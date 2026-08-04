package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/commerce/armcommerce"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"
)

const (
	defaultTimeout = 30 * time.Second
)

// GCP Instance Specifications (Predefined knowledge)
type GCPInstanceSpec struct {
	Family   string
	Type     string
	VCPUs    int
	MemoryGB float64
}

var gcpInstanceSpecs = map[string]GCPInstanceSpec{
	// General Purpose
	"n1-standard":  {Family: "n1", Type: "standard", VCPUs: 1, MemoryGB: 3.75},
	"n2-standard":  {Family: "n2", Type: "standard", VCPUs: 2, MemoryGB: 8},
	"e2-standard":  {Family: "e2", Type: "standard", VCPUs: 2, MemoryGB: 8},
	// Memory Optimized
	"n1-highmem":   {Family: "n1", Type: "highmem", VCPUs: 2, MemoryGB: 13},
	"n2-highmem":   {Family: "n2", Type: "highmem", VCPUs: 2, MemoryGB: 16},
	// Compute Optimized
	"n1-highcpu":   {Family: "n1", Type: "highcpu", VCPUs: 2, MemoryGB: 1.8},
	"c2-standard":  {Family: "c2", Type: "standard", VCPUs: 4, MemoryGB: 16},
	// GPU Instances
	"a2-highgpu":   {Family: "a2", Type: "highgpu", VCPUs: 12, MemoryGB: 85},
}

// SpecMapEntry represents normalized instance data
type SpecMapEntry struct {
	ID           string  `json:"id"`
	Provider     string  `json:"provider"`
	Name         string  `json:"name"`
	Region       string  `json:"region"`
	VCpus        int     `json:"vcpus"`
	MemoryGB     float64 `json:"memoryGB"`
	StorageType  string  `json:"storageType"`
	SpotPrice    float64 `json:"spotPrice"`
	LastUpdated  string  `json:"lastUpdated"`
	InstanceType string  `json:"instanceType"`
}

type SpecMap struct {
	mu      sync.RWMutex
	Entries map[string]SpecMapEntry
}

func NewSpecMap() *SpecMap {
	return &SpecMap{
		Entries: make(map[string]SpecMapEntry),
	}
}

func (m *SpecMap) AddEntry(entry SpecMapEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	id := fmt.Sprintf("%s:%s:%s", 
		strings.ToLower(entry.Provider),
		strings.ToLower(entry.Region),
		strings.ToLower(entry.Name))
	
	m.Entries[id] = entry
}

func (m *SpecMap) SaveToFile(filename string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	data, err := json.MarshalIndent(m.Entries, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filename, data, 0644)
}

// AWS Fetcher
func fetchAWSInstances(specMap *SpecMap) error {
	ctx := context.TODO()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}

	client := ec2.NewFromConfig(cfg)

	paginator := ec2.NewDescribeInstanceTypesPaginator(client, &ec2.DescribeInstanceTypesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}

		for _, it := range page.InstanceTypes {
			priceInput := &ec2.DescribeSpotPriceHistoryInput{
				InstanceTypes:       []types.InstanceType{it.InstanceType},
				ProductDescriptions: []string{"Linux/UNIX"},
				StartTime:          aws.Time(time.Now()),
			}

			priceResult, err := client.DescribeSpotPriceHistory(ctx, priceInput)
			if err != nil || len(priceResult.SpotPriceHistory) == 0 {
				continue
			}

			spotPrice, _ := strconv.ParseFloat(*priceResult.SpotPriceHistory[0].SpotPrice, 64)

			entry := SpecMapEntry{
				Provider:     "AWS",
				Name:         string(it.InstanceType),
				Region:       cfg.Region,
				VCpus:        int(aws.ToInt32(it.VCpuInfo.DefaultVCpus)),
				MemoryGB:     float64(aws.ToInt64(it.MemoryInfo.SizeInMiB)) / 1024,
				StorageType:  mapStorageType(it.InstanceStorageInfo),
				SpotPrice:    spotPrice,
				LastUpdated:  time.Now().Format(time.RFC3339),
				InstanceType: "AWS",
			}

			specMap.AddEntry(entry)
		}
	}
	return nil
}

// Azure Fetcher
func fetchAzureInstances(subscriptionID string, specMap *SpecMap) error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return err
	}

	computeClient, err := armcompute.NewVirtualMachineSizesClient(subscriptionID, cred, nil)
	if err != nil {
		return err
	}

	pager := computeClient.NewListPager("eastus", nil)
	for pager.More() {
		page, err := pager.NextPage(context.Background())
		if err != nil {
			return err
		}

		commerceClient, err := armcommerce.NewRateCardClient(subscriptionID, cred, nil)
		if err != nil {
			return err
		}

		resp, err := commerceClient.Get(context.Background(), "OfferDurableId eq 'MS-AZR-0003P'", nil)
		if err != nil {
			return err
		}

		for _, size := range page.Value {
			var price float64
			for _, meter := range resp.Meters {
				if *meter.MeterName == *size.Name+" Spot" && meter.MeterRates != nil {
					price = float64(*meter.MeterRates["0"])
					break
				}
			}

			entry := SpecMapEntry{
				Provider:    "Azure",
				Name:        *size.Name,
				Region:      "eastus",
				VCpus:       int(*size.NumberOfCores),
				MemoryGB:    float64(*size.MemoryInMB) / 1024,
				StorageType: "SSD",
				SpotPrice:   price,
				LastUpdated: time.Now().Format(time.RFC3339),
				InstanceType: "Azure",
			}

			specMap.AddEntry(entry)
		}
	}
	return nil
}

// GCP Fetcher
func fetchGCPInstances(projectID string, specMap *SpecMap) error {
	log.Println("Starting GCP instance fetch...")
	startTime := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if _, err := os.Stat("credentials.json"); os.IsNotExist(err) {
		log.Printf("GCP credentials file not found")
		return nil
	}

	billingService, err := cloudbilling.NewService(ctx, option.WithCredentialsFile("credentials.json"))
	if err != nil {
		return fmt.Errorf("GCP service creation failed: %v", err)
	}

	skus, err := billingService.Services.Skus.List("services/6F81-5844-456A").Do()
	if err != nil {
		return fmt.Errorf("SKU list failed: %v", err)
	}

	processedCount := 0
	for _, sku := range skus.Skus {
		if !isValidPreemptibleSKU(sku) {
			continue
		}

		entry, valid := parseGCPEntry(sku)
		if valid {
			specMap.AddEntry(entry)
			processedCount++
		}
	}

	log.Printf("GCP fetch complete. Processed %d/%d SKUs in %v", 
		processedCount, len(skus.Skus), time.Since(startTime))
	return nil
}

func isValidPreemptibleSKU(sku *cloudbilling.Sku) bool {
	return strings.Contains(sku.Description, "Preemptible") &&
		strings.Contains(sku.Description, "Instance Core") &&
		!strings.Contains(sku.Description, "Commitment")
}

func parseGCPEntry(sku *cloudbilling.Sku) (SpecMapEntry, bool) {
	entry := SpecMapEntry{
		Provider:     "GCP",
		LastUpdated:  time.Now().Format(time.RFC3339),
		InstanceType: "GCP",
		Region:       firstItemOr(sku.ServiceRegions, "global"),
		StorageType:  "SSD",
	}

	instanceType, ok := extractInstanceType(sku.Description)
	if !ok {
		return SpecMapEntry{}, false
	}
	entry.Name = instanceType

	if spec, exists := gcpInstanceSpecs[instanceType]; exists {
		entry.VCpus = spec.VCPUs
		entry.MemoryGB = spec.MemoryGB
	} else {
		vcpus, memoryGB, ok := parseCustomInstance(instanceType)
		if !ok {
			return SpecMapEntry{}, false
		}
		entry.VCpus = vcpus
		entry.MemoryGB = memoryGB
	}

	entry.SpotPrice = extractGCPSpotPrice(sku)
	return entry, true
}

func extractInstanceType(desc string) (string, bool) {
	parts := strings.Split(desc, " ")
	if len(parts) < 4 {
		return "", false
	}
	
	typeParts := strings.Split(parts[1], "-")
	if len(typeParts) < 2 {
		return "", false
	}
	
	return strings.ToLower(strings.Join(typeParts[:2], "-")), true
}

func parseCustomInstance(instanceType string) (int, float64, bool) {
	parts := strings.Split(instanceType, "-")
	if len(parts) < 3 || parts[0] != "custom" {
		return 0, 0, false
	}

	vcpus, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}

	memoryMB, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, 0, false
	}

	return vcpus, float64(memoryMB) / 1024, true
}

func extractGCPSpotPrice(sku *cloudbilling.Sku) float64 {
	for _, tier := range sku.PricingInfo {
		if tier.PricingExpression != nil && len(tier.PricingExpression.TieredRates) > 0 {
			rate := tier.PricingExpression.TieredRates[0]
			return float64(rate.UnitPrice.Units) + float64(rate.UnitPrice.Nanos)/1e9
		}
	}
	return 0
}

func firstItemOr(items []string, def string) string {
	if len(items) > 0 {
		return items[0]
	}
	return def
}

func mapStorageType(info *types.InstanceStorageInfo) string {
	if info == nil || len(info.Disks) == 0 {
		return "EBS"
	}
	switch string(info.Disks[0].Type) {
	case "hdd":
		return "HDD"
	case "ssd":
		return "SSD"
	default:
		return "Standard"
	}
}

func main() {
	specMap := NewSpecMap()

	if err := fetchAWSInstances(specMap); err != nil {
		log.Printf("AWS fetch failed: %v", err)
	}

	if err := fetchAzureInstances("b1e773bb-e34f-460f-86c1-ad8e0c5c5514", specMap); err != nil {
		log.Printf("Azure fetch failed: %v", err)
	}

	if err := fetchGCPInstances("electric-totem-455409-u5", specMap); err != nil {
		log.Printf("GCP fetch failed: %v", err)
	}

	if err := specMap.SaveToFile("spec_map.json"); err != nil {
		log.Fatalf("Failed to save spec map: %v", err)
	}

	fmt.Println("Spec map updated successfully with", len(specMap.Entries), "entries")
}