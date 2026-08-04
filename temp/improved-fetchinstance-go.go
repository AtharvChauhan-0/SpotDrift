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

type UIState struct {
	app             *tview.Application
	providerList    *tview.List
	instanceList    *tview.List
	resultsTable    *tview.Table
	statusTextView  *tview.TextView
	allInstances    []Instance
	selectedCloud   string
	selectedInstance *Instance
}

const (
	vCPUTolerance = 1
	memTolerance  = 0.5
	defaultTimeout = 30 * time.Second // Default timeout for API calls
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
	log.Println("Starting AWS instance fetch...")
	startTime := time.Now()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Printf("AWS config error: %v", err)
		return nil
	}
	log.Printf("AWS configuration loaded for region: %s", cfg.Region)

	client := ec2.NewFromConfig(cfg)
	var instances []Instance

	log.Println("Fetching AWS instance types...")
	paginatorStartTime := time.Now()
	
	paginator := ec2.NewDescribeInstanceTypesPaginator(client, &ec2.DescribeInstanceTypesInput{})
	pageCount := 0
	
	for paginator.HasMorePages() {
		pageCtx, pageCancel := context.WithTimeout(context.Background(), defaultTimeout)
		page, err := paginator.NextPage(pageCtx)
		pageCancel()
		
		if err != nil {
			log.Printf("AWS instance types error on page %d: %v", pageCount, err)
			continue
		}
		
		pageCount++
		log.Printf("Processing AWS instance types page %d with %d instances", pageCount, len(page.InstanceTypes))
		
		for _, it := range page.InstanceTypes {
			var storageType string
			if it.InstanceStorageInfo != nil && it.InstanceStorageInfo.Disks != nil && len(it.InstanceStorageInfo.Disks) > 0 {
				storageType = string(it.InstanceStorageInfo.Disks[0].Type)
			}

			// Create timeout context for price fetch
			priceCtx, priceCancel := context.WithTimeout(context.Background(), 5*time.Second)
			price, err := getAWSSpotPrice(priceCtx, client, string(it.InstanceType))
			priceCancel()
			
			if err != nil {
				log.Printf("Failed to get spot price for %s: %v", it.InstanceType, err)
			}
			
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
		
		// Add a small delay between pages to avoid rate limiting
		time.Sleep(100 * time.Millisecond)
	}
	
	log.Printf("AWS instance fetch complete. Got %d instances in %v (pagination took %v)", 
		len(instances), time.Since(startTime), time.Since(paginatorStartTime))
	
	return instances
}

func getAWSSpotPrice(ctx context.Context, client *ec2.Client, instanceType string) (float64, error) {
	input := &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []types.InstanceType{types.InstanceType(instanceType)},
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:          aws.Time(time.Now()),
	}

	result, err := client.DescribeSpotPriceHistory(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("spot price API error: %w", err)
	}
	
	if len(result.SpotPriceHistory) == 0 {
		return 0, fmt.Errorf("no spot price history available")
	}

	return strconv.ParseFloat(*result.SpotPriceHistory[0].SpotPrice, 64)
}

// GCP Instance Fetching
func fetchGCPInstances(projectID string) []Instance {
	log.Println("Starting GCP instance fetch...")
	startTime := time.Now()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Check if credentials file exists
	if _, err := os.Stat("credentials.json"); os.IsNotExist(err) {
		log.Printf("GCP credentials file 'credentials.json' not found. Skipping GCP instances.")
		return nil
	}

	log.Println("Creating GCP billing service...")
	billingService, err := cloudbilling.NewService(ctx, option.WithCredentialsFile("credentials.json"))
	if err != nil {
		log.Printf("Failed to create GCP billing service: %v", err)
		return nil
	}

	var instances []Instance
	// The List method requires a parent parameter (service name)
	skuService := "services/6F81-5844-456A" // Compute Engine service
	
	log.Println("Fetching GCP SKUs...")
	skus, err := billingService.Services.Skus.List(skuService).Context(ctx).Do()
	if err != nil {
		log.Printf("Failed to fetch GCP SKUs: %v", err)
		return nil
	}
	
	log.Printf("Processing %d GCP SKUs...", len(skus.Skus))
	preemptibleCount := 0
	
	for _, sku := range skus.Skus {
		if strings.Contains(sku.Description, "Preemptible") {
			preemptibleCount++
			
			// Extract instance type from description
			parts := strings.Split(sku.Description, " ")
			if len(parts) < 3 {
				continue
			}
			
			machineType := parts[1]
			region := "us-central1" // Default region
			
			// Extract vCPUs and memory from description
			vcpus := 0
			memoryGB := 0.0
			
			if strings.Contains(machineType, "-") {
				specs := strings.Split(machineType, "-")
				if len(specs) >= 3 {
					vcpus, _ = strconv.Atoi(specs[1])
					memoryGB, _ = strconv.ParseFloat(specs[2], 64)
				}
			}
			
			price := 0.0
			for _, tier := range sku.PricingInfo {
				if tier.PricingExpression != nil && len(tier.PricingExpression.TieredRates) > 0 {
					price = float64(tier.PricingExpression.TieredRates[0].UnitPrice.Units) + 
						float64(tier.PricingExpression.TieredRates[0].UnitPrice.Nanos) / 1e9
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
	log.Println("Starting Azure instance fetch...")
	startTime := time.Now()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // Azure API can be slow
	defer cancel()

	log.Println("Creating Azure credentials...")
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Printf("Azure auth error: %v", err)
		return nil
	}

	log.Printf("Creating Azure rate card client for subscription: %s", subscriptionID)
	// Using RateCardClient instead of UsageManagementClient which was undefined
	client, err := armcommerce.NewRateCardClient(subscriptionID, cred, nil)
	if err != nil {
		log.Printf("Azure client error: %v", err)
		return nil
	}

	var instances []Instance
	
	// Create filter for VMs
	filter := "OfferDurableId eq 'MS-AZR-0003P' and Currency eq 'USD' and Locale eq 'en-US' and RegionInfo eq 'US'"
	
	log.Println("Fetching Azure rate card information...")
	resp, err := client.Get(ctx, filter, nil)
	if err != nil {
		log.Printf("Azure rate card error: %v", err)
		return nil
	}

	if resp.Meters == nil {
		log.Println("Azure returned no meter information")
		return nil
	}
	
	log.Printf("Processing %d Azure meters...", len(resp.Meters))
	vmCount := 0
	lowPriorityCount := 0
	
	for _, meter := range resp.Meters {
		if meter.MeterCategory == nil || meter.MeterSubCategory == nil {
			continue
		}
		
		if *meter.MeterCategory == "Virtual Machines" {
			vmCount++
			
			if strings.Contains(*meter.MeterSubCategory, "Low Priority") {
				lowPriorityCount++
				
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
					// Fix: Convert float32 to float64
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
	}
	
	log.Printf("Azure instance fetch complete. Found %d low priority out of %d VM meters. Got %d instances in %v",
		lowPriorityCount, vmCount, len(instances), time.Since(startTime))
	
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
		// Initialize TUI
		app := tview.NewApplication()
	
		// Create UI components
		state := &UIState{
			app:            app,
			providerList:   tview.NewList(),
			instanceList:   tview.NewList(),
			resultsTable:   tview.NewTable(),
			statusTextView: tview.NewTextView(),
		}
	
		// Configure UI layout
		flex := tview.NewFlex().
			AddItem(createProviderPane(state), 0, 1, true).
			AddItem(createInstancePane(state), 0, 2, false).
			AddItem(createResultsPane(state), 0, 3, false)
	
		// Configure status bar
		statusBar := tview.NewFlex().
			AddItem(state.statusTextView, 0, 1, false)
		
		mainLayout := tview.NewFlex().
			SetDirection(tview.FlexRow).
			AddItem(flex, 0, 1, true).
			AddItem(statusBar, 1, 1, false)

	log.Println("Starting cloud instance comparison tool...")
	startTime := time.Now()
	
	log.Println("Fetching AWS instances...")
	awsStartTime := time.Now()
	awsInstances := fetchAWSInstances()
	log.Printf("AWS fetch completed in %v, got %d instances", time.Since(awsStartTime), len(awsInstances))
	
	log.Println("Fetching GCP instances...")
	gcpStartTime := time.Now()
	gcpInstances := fetchGCPInstances("electric-totem-455409-u5")
	log.Printf("GCP fetch completed in %v, got %d instances", time.Since(gcpStartTime), len(gcpInstances))
	
	log.Println("Fetching Azure instances...")
	azureStartTime := time.Now()
	azureInstances := fetchAzureInstances("7089df5a-2131-4f99-bc3b-4349f9b4af47")
	log.Printf("Azure fetch completed in %v, got %d instances", time.Since(azureStartTime), len(azureInstances))

	allInstances := append(append(awsInstances, gcpInstances...), azureInstances...)
	log.Printf("Combined all clouds: %d total instances", len(allInstances))
	
	// Normalize instances
	log.Println("Normalizing instances...")
	normalizeStartTime := time.Now()
	normalized := normalizeInstances(allInstances)
	log.Printf("Normalization completed in %v, created %d normalized instance groups", 
		time.Since(normalizeStartTime), len(normalized))
	
	// Find equivalents for AWS t2.micro
	log.Println("Finding equivalents for AWS t2.micro...")
	var target *Instance
	for i, inst := range allInstances {
		if inst.Provider == "AWS" && inst.Name == "t2.micro" {
			target = &allInstances[i]
			break
		}
	}
	
	if target != nil {
		log.Printf("Found t2.micro: %+v", *target)
		equivalents := findEquivalents(*target, allInstances)
		fmt.Println("\nAWS t2.micro equivalents:")
		for _, eq := range equivalents {
			fmt.Printf("  - %s %s: $%.4f (%s)\n", 
				eq.Provider, 
				eq.Name, 
				eq.SpotPrice,
				eq.Region)
		}
		log.Printf("Found %d equivalents across clouds", len(equivalents))
	} else {
		log.Println("AWS t2.micro instance not found in the data")
	}
	
	// Save data to JSON
	log.Println("Saving normalized data to JSON...")
	saveNormalizedToJSON(normalized, "normalized_instances.json")
	log.Printf("Total execution time: %v", time.Since(startTime))
	log.Println("Program completed successfully")

	app.QueueUpdateDraw(func() {
		state.updateStatus("Data loaded successfully!")
		state.populateProviders()
	})
}

// Run application
if err := app.SetRoot(mainLayout, true).EnableMouse(true).Run(); err != nil {
	panic(err)

}

func createProviderPane(state *UIState) *tview.Flex {
	state.providerList.ShowSecondaryText(false)
	state.providerList.SetBorder(true).SetTitle(" Cloud Providers ")
	
	helpText := tview.NewTextView().
		SetText("↑/↓: Navigate\nEnter: Select\nEsc: Back")
	
	return tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(state.providerList, 0, 1, true).
		AddItem(helpText, 4, 1, false)
}

func createInstancePane(state *UIState) *tview.Flex {
	state.instanceList.ShowSecondaryText(false)
	state.instanceList.SetBorder(true).SetTitle(" Instances ")
	
	detailsView := tview.NewTextView().
		SetDynamicColors(true).
		SetBorder(true).
		SetTitle(" Instance Details ")
	
	return tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(state.instanceList, 0, 2, true).
		AddItem(detailsView, 10, 1, false)
}

func createResultsPane(state *UIState) *tview.Flex {
	state.resultsTable.SetBorder(true).SetTitle(" Equivalent Instances ")
	state.resultsTable.SetSelectable(true, false)
	
	// Configure table headers
	state.resultsTable.SetCell(0, 0, tview.NewTableCell("Provider").SetSelectable(false))
	state.resultsTable.SetCell(0, 1, tview.NewTableCell("Instance").SetSelectable(false))
	state.resultsTable.SetCell(0, 2, tview.NewTableCell("Price").SetSelectable(false))
	state.resultsTable.SetCell(0, 3, tview.NewTableCell("Region").SetSelectable(false))
	
	return tview.NewFlex().
		AddItem(state.resultsTable, 0, 1, false)
}

func (state *UIState) populateProviders() {
	state.providerList.Clear()
	state.providerList.AddItem("AWS", "", 'a', func() {
		state.selectedCloud = "AWS"
		state.populateInstances()
	})
	state.providerList.AddItem("GCP", "", 'g', func() {
		state.selectedCloud = "GCP"
		state.populateInstances()
	})
	state.providerList.AddItem("Azure", "", 'z', func() {
		state.selectedCloud = "Azure"
		state.populateInstances()
	})
	state.app.SetFocus(state.providerList)
}

func (state *UIState) populateInstances() {
	state.instanceList.Clear()
	for _, inst := range state.allInstances {
		if inst.Provider == state.selectedCloud {
			instance := inst // Capture local copy
			state.instanceList.AddItem(inst.Name, "", 0, func() {
				state.selectedInstance = &instance
				state.showEquivalents()
			})
		}
	}
	state.app.SetFocus(state.instanceList)
}

func (state *UIState) showEquivalents() {
	state.resultsTable.Clear()
	row := 1 // Start after header
	
	equivalents := findEquivalents(*state.selectedInstance, state.allInstances)
	
	for _, eq := range equivalents {
		price := fmt.Sprintf("$%.4f", eq.SpotPrice)
		if eq.SpotPrice == 0 {
			price = "N/A"
		}
		
		state.resultsTable.SetCell(row, 0, tview.NewTableCell(eq.Provider))
		state.resultsTable.SetCell(row, 1, tview.NewTableCell(eq.Name))
		state.resultsTable.SetCell(row, 2, tview.NewTableCell(price))
		state.resultsTable.SetCell(row, 3, tview.NewTableCell(eq.Region))
		row++
	}
	
	state.updateStatus(fmt.Sprintf("Found %d equivalents for %s/%s",
		len(equivalents), state.selectedCloud, state.selectedInstance.Name))
	state.app.SetFocus(state.resultsTable)
}

func (state *UIState) updateStatus(message string) {
	state.statusTextView.Clear()
	fmt.Fprintf(state.statusTextView, "[green]Status:[-] %s", message)
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
	if mapping, exists := familyMapping[provider]; exists {
		for prefix, family := range mapping {
			if strings.HasPrefix(name, prefix) {
				return family
			}
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
	file, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Printf("Error marshalling JSON: %v", err)
		return
	}
	
	err = os.WriteFile(filename, file, 0644)
	if err != nil {
		log.Printf("Error writing to file %s: %v", filename, err)
		return
	}
	
	log.Printf("Successfully saved normalized data to %s (%d bytes)", filename, len(file))
	fmt.Printf("Normalized data saved to %s\n", filename)
}
