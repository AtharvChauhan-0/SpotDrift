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
			if it.InstanceStorageInfo != nil && it.InstanceStorageInfo.Disks != nil && len(it.InstanceStorageInfo.Disks) > 0 {
				storageType = string(it.InstanceStorageInfo.Disks[0].Type)
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

// GCP Instance Fetching
// GCP Instance Fetching
func fetchGCPInstances(projectID string) []Instance {
	log.Println("Starting GCP instance fetch...")
	startTime := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if _, err := os.Stat("credentials.json"); os.IsNotExist(err) {
		log.Printf("GCP credentials file 'credentials.json' not found. Skipping GCP instances.")
		return nil
	}

	billingService, err := cloudbilling.NewService(ctx, option.WithCredentialsFile("credentials.json"))
	if err != nil {
		log.Printf("Failed to create GCP billing service: %v", err)
		return nil
	}

	var instances []Instance
	skuService := "services/6F81-5844-456A"

	skus, err := billingService.Services.Skus.List(skuService).Context(ctx).Do()
	if err != nil {
		log.Printf("Failed to fetch GCP SKUs: %v", err)
		return nil
	}

	preemptibleCount := 0

	for _, sku := range skus.Skus {
		if strings.Contains(sku.Description, "Preemptible") {
			preemptibleCount++

			var (
				machineType string
				vcpus       int
				memoryGB    float64
				region      string
			)

			// Extract metadata
			for key, value := range sku.Metadata {
				switch key {
				case "machineType":
					machineType = value
				case "cores":
					vcpus, _ = strconv.Atoi(value)
				case "memory":
					memoryMB, _ := strconv.Atoi(value)
					memoryGB = float64(memoryMB) / 1024
				}
			}

			// Get region from service regions
			if len(sku.ServiceRegions) > 0 {
				region = sku.ServiceRegions[0]
			} else {
				region = "global"
			}

			// Parse machineType if metadata didn't provide cores/memory
			if machineType != "" && (vcpus == 0 || memoryGB == 0) {
				parts := strings.Split(machineType, "-")
				if len(parts) >= 3 {
					switch parts[1] {
					case "standard", "highmem", "highcpu":
						if parsedVCpus, err := strconv.Atoi(parts[2]); err == nil {
							vcpus = parsedVCpus
							switch parts[1] {
							case "standard":
								memoryGB = float64(vcpus) * 4
							case "highmem":
								memoryGB = float64(vcpus) * 8
							case "highcpu":
								memoryGB = float64(vcpus)
							}
						}
					case "custom":
						if len(parts) >= 4 {
							if parsedVCpus, err := strconv.Atoi(parts[2]); err == nil {
								vcpus = parsedVCpus
							}
							if parsedMemoryMB, err := strconv.Atoi(parts[3]); err == nil {
								memoryGB = float64(parsedMemoryMB) / 1024
							}
						}
					}
				}
			}

			// Get price
			price := 0.0
			for _, tier := range sku.PricingInfo {
				if tier.PricingExpression != nil && len(tier.PricingExpression.TieredRates) > 0 {
					price = float64(tier.PricingExpression.TieredRates[0].UnitPrice.Units) +
						float64(tier.PricingExpression.TieredRates[0].UnitPrice.Nanos)/1e9
					break
				}
			}

			instances = append(instances, Instance{
				Provider:    "GCP",
				Name:        machineType,
				VCpus:       vcpus,
				MemoryGB:    memoryGB,
				StorageType: "SSD",
				NetworkPerf: "Standard",
				SpotPrice:   price,
				Region:      region,
			})
		}
	}

	log.Printf("GCP instance fetch complete. Found %d preemptible out of %d total SKUs. Got %d instances in %v",
		preemptibleCount, len(skus.Skus), len(instances), time.Since(startTime))

	return instances
}
// Azure Instance Fetching
func fetchAzureInstances(subscriptionID string) []Instance {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Printf("Azure auth error: %v", err)
		return nil
	}

	// Using RateCardClient instead of UsageManagementClient which was undefined
	client, err := armcommerce.NewRateCardClient(subscriptionID, cred, nil)
	if err != nil {
		log.Printf("Azure client error: %v", err)
		return nil
	}

	var instances []Instance
	
	// Create filter for VMs
	filter := "OfferDurableId eq 'MS-AZR-0003P' and Currency eq 'USD' and Locale eq 'en-US' and RegionInfo eq 'US'"
	resp, err := client.Get(context.TODO(), filter, nil)
	if err != nil {
		log.Printf("Azure rate card error: %v", err)
		return nil
	}

	for _, meter := range resp.Meters {
		if meter.MeterCategory != nil && *meter.MeterCategory == "Virtual Machines" && 
		   meter.MeterSubCategory != nil && strings.Contains(*meter.MeterSubCategory, "Low Priority") {
			
			name := ""
			if meter.MeterName != nil {
				name = *meter.MeterName
			}
			
			region := ""
			if meter.MeterRegion != nil {
				region = *meter.MeterRegion
			}
			
			price := 0.0
			if meter.MeterRates != nil && meter.MeterRates["0"] != nil {
				price = float64(*meter.MeterRates["0"])
			}
			
			instances = append(instances, Instance{
				Provider:    "Azure",
				Name:        name,
				VCpus:       extractvCPUs(name),
				MemoryGB:    extractMemory(name),
				StorageType: "SSD",
				NetworkPerf: "Moderate",
				SpotPrice:   price,
				Region:      region,
			})
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