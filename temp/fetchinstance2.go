package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/commerce/armcommerce"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

type Instance struct {
	Provider          string  `json:"provider"`
	Name              string  `json:"name"`
	VCpus             int     `json:"vcpus"`
	MemoryGB          float64 `json:"memoryGB"`
	StorageType       string  `json:"storageType"`
	NetworkPerf       string  `json:"networkPerf"`
	SpotPrice         float64 `json:"spotPrice"`
	Region            string  `json:"region"`
}

type NormalizedInstance struct {
	Family    string         `json:"family"`
	VCpus     int            `json:"vcpus"`
	MemoryGB  float64        `json:"memoryGB"`
	Matches   []CloudInstance `json:"matches"`
}

type CloudInstance struct {
	Provider   string  `json:"provider"`
	Name       string  `json:"name"`
	SpotPrice  float64 `json:"spotPrice"`
	Region     string  `json:"region"`
}

const (
	vCPUTolerance = 1
	memTolerance  = 0.5
)

var familyMapping = map[string]map[string]string{
	"AWS": {
		"t2.":    "burstable",
		"m5.":    "general-purpose",
		"c5.":    "compute-optimized",
		"r5.":    "memory-optimized",
		"g4dn.":  "gpu",
	},
	"GCP": {
		"e2-":     "burstable",
		"n2-":     "general-purpose",
		"c2-":     "compute-optimized",
		"m2-":     "memory-optimized",
		"a2-":     "gpu",
	},
	"Azure": {
		"B":       "burstable",
		"Dv4":     "general-purpose",
		"F":       "compute-optimized",
		"E":       "memory-optimized",
		"NC":      "gpu",
	},
}

// AWS Spot Price Fetcher
func fetchAWSInstances() []Instance {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Printf("AWS config error: %v", err)
		return nil
	}

	client := ec2.NewFromConfig(cfg)
	var instances []Instance

	paginator := ec2.NewDescribeInstanceTypesPaginator(client, &ec2.DescribeInstanceTypesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(context.TODO())
		if err != nil {
			log.Printf("AWS instance types error: %v", err)
			continue
		}

		for _, it := range page.InstanceTypes {
			var storageType string
			if it.InstanceStorageInfo != nil && len(it.InstanceStorageInfo.StorageTypes) > 0 {
				storageType = string(it.InstanceStorageInfo.StorageTypes[0])
			}

			price, _ := getAWSSpotPrice(client, string(it.InstanceType))
			
			instances = append(instances, Instance{
				Provider:          "AWS",
				Name:             string(it.InstanceType),
				VCpus:            int(aws.ToInt32(it.VCpuInfo.DefaultVCpus)),
				MemoryGB:         float64(aws.ToInt64(it.MemoryInfo.SizeInMiB)) / 1024,
				StorageType:      mapStorageType(storageType),
				NetworkPerf:      aws.ToString(it.NetworkInfo.NetworkPerformance),
				SpotPrice:        price,
				Region:           cfg.Region,
			})
		}
	}
	return instances
}

func getAWSSpotPrice(client *ec2.Client, instanceType string) (float64, error) {
	input := &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []types.InstanceType{types.InstanceType(instanceType)},
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:          aws.Time(time.Now()),
	}

	result, err := client.DescribeSpotPriceHistory(context.TODO(), input)
	if err != nil || len(result.SpotPriceHistory) == 0 {
		return 0, fmt.Errorf("price not found")
	}

	return strconv.ParseFloat(*result.SpotPriceHistory[0].SpotPrice, 64)
}

// GCP Instance Fetching (Fixed)
func getGCPSpotPrice(svc *cloudbilling.APIService, machineType, zone string) float64 {
	skus, _ := svc.Services.Skus.List().Do()
	
	for _, sku := range skus.Skus {
		if strings.Contains(sku.Description, "Preemptible") && 
			strings.Contains(sku.Description, machineType) {
			for _, tier := range sku.PricingInfo {
				if tier.PricingExpression != nil && len(tier.PricingExpression.TieredRates) > 0 {
					return float64(tier.PricingExpression.TieredRates[0].UnitPrice.Nanos) / 1e9
				}
			}
		}
	}
	return 0
}

// Azure Instance Fetching (Fixed)
func fetchAzureInstances(subscriptionID string) []Instance {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Printf("Azure auth error: %v", err)
		return nil
	}

	client, err := armcommerce.NewUsageManagementClient(subscriptionID, cred, nil)
	if err != nil {
		log.Printf("Azure client error: %v", err)
		return nil
	}

	var instances []Instance
	pager := client.NewListPager(&armcommerce.UsageManagementClientListOptions{})

	for pager.More() {
		page, err := pager.NextPage(context.TODO())
		if err != nil {
			log.Printf("Azure pricing error: %v", err)
			continue
		}

		for _, item := range page.Value {
			if item.MeterRates == nil || item.Name == nil {
				continue
			}
			
			if strings.Contains(*item.MeterName, "Spot") {
				price, _ := strconv.ParseFloat(*item.MeterRates["0"], 64)
				instances = append(instances, Instance{
					Provider:    "Azure",
					Name:       *item.MeterName,
					VCpus:      extractvCPUs(*item.MeterName),
					MemoryGB:   extractMemory(*item.MeterName),
					StorageType: "SSD",
					NetworkPerf: "Moderate",
					SpotPrice:  price,
					Region:     *item.MeterRegion,
				})
			}
		}
	}
	return instances
}
// Helper functions
func mapStorageType(desc string) string {
	switch {
	case strings.Contains(desc, "SSD"):
		return "SSD"
	case strings.Contains(desc, "HDD"):
		return "HDD"
	case strings.Contains(desc, "NVMe"):
		return "NVMe"
	default:
		return "Standard"
	}
}

func extractvCPUs(name string) int {
	parts := strings.Split(name, "_")
	if len(parts) > 1 {
		if v, err := strconv.Atoi(parts[1]); err == nil {
			return v
		}
	}
	return 0
}

func extractMemory(name string) float64 {
	parts := strings.Split(name, "_")
	if len(parts) > 2 {
		if v, err := strconv.ParseFloat(parts[2], 64); err == nil {
			return v
		}
	}
	return 0
}

func main() {
	awsInstances := fetchAWSInstances()
	gcpInstances := fetchGCPInstances("electric-totem-455409-u5")
	azureInstances := fetchAzureInstances("7089df5a-2131-4f99-bc3b-4349f9b4af47")

	allInstances := append(append(awsInstances, gcpInstances...), azureInstances...)
	
	// Normalize instances
	normalized := normalizeInstances(allInstances)
	
	// Find equivalents for AWS t2.micro
	var target *Instance
	for i, inst := range allInstances {
		if inst.Provider == "AWS" && inst.Name == "t2.micro" {
			target = &allInstances[i]
			break
		}
	}
	
	if target != nil {
		equivalents := findEquivalents(*target, allInstances)
		fmt.Println("\nAWS t2.micro equivalents:")
		for _, eq := range equivalents {
			fmt.Printf("  - %s %s: $%.4f (%s)\n", 
				eq.Provider, 
				eq.Name, 
				eq.SpotPrice,
				eq.Region)
		}
	}
	
	saveNormalizedToJSON(normalized, "normalized_instances.json")
}

func normalizeInstances(instances []Instance) []NormalizedInstance {
	normalizedMap := make(map[string]NormalizedInstance)
	
	for _, inst := range instances {
		family := getInstanceFamily(inst.Provider, inst.Name)
		key := fmt.Sprintf("%s-%d-%.1f", family, inst.VCpus, inst.MemoryGB)
		
		if entry, exists := normalizedMap[key]; exists {
			entry.Matches = append(entry.Matches, CloudInstance{
				Provider:  inst.Provider,
				Name:      inst.Name,
				SpotPrice: inst.SpotPrice,
				Region:    inst.Region,
			})
			normalizedMap[key] = entry
		} else {
			normalizedMap[key] = NormalizedInstance{
				Family:   family,
				VCpus:    inst.VCpus,
				MemoryGB: inst.MemoryGB,
				Matches: []CloudInstance{{
					Provider:  inst.Provider,
					Name:      inst.Name,
					SpotPrice: inst.SpotPrice,
					Region:    inst.Region,
				}},
			}
		}
	}
	
	result := make([]NormalizedInstance, 0, len(normalizedMap))
	for _, v := range normalizedMap {
		result = append(result, v)
	}
	return result
}

func findEquivalents(target Instance, instances []Instance) []CloudInstance {
	var matches []CloudInstance
	targetFamily := getInstanceFamily(target.Provider, target.Name)
	
	for _, inst := range instances {
		if inst.Provider == target.Provider {
			continue
		}
		
		instFamily := getInstanceFamily(inst.Provider, inst.Name)
		if instFamily != targetFamily {
			continue
		}
		
		vDiff := absInt(inst.VCpus - target.VCpus)
		mDiff := math.Abs(inst.MemoryGB - target.MemoryGB)
		
		if vDiff <= vCPUTolerance && mDiff <= memTolerance {
			matches = append(matches, CloudInstance{
				Provider:  inst.Provider,
				Name:      inst.Name,
				SpotPrice: inst.SpotPrice,
				Region:    inst.Region,
			})
		}
	}
	return matches
}

func getInstanceFamily(provider, name string) string {
	for prefix, family := range familyMapping[provider] {
		if strings.HasPrefix(name, prefix) {
			return family
		}
	}
	return "other"
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func saveNormalizedToJSON(data []NormalizedInstance, filename string) {
	file, _ := json.MarshalIndent(data, "", "  ")
	os.WriteFile(filename, file, 0644)
	fmt.Printf("Normalized data saved to %s\n", filename)
}