package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type Instance struct {
	Name             string `json:"name"`
	VCpus            int    `json:"vcpus"`
	MemoryGB         float64 `json:"memory_gb"`
	StorageType      string `json:"storage_type"`
	NetworkPerformance string `json:"network_performance"`
	PriceUSD 		string `json:"price_usd,omitempty"`
}

// Normalize storage types for easier comparison
func mapStorageType(storage string) string {
	lowerStorage := strings.ToLower(storage)

	if strings.Contains(lowerStorage, "ssd") {
		if strings.Contains(lowerStorage, "premium") || strings.Contains(lowerStorage, "io1") || strings.Contains(lowerStorage, "io2") {
			return "High-Performance SSD"
		}
		return "General-Purpose SSD"
	}

	if strings.Contains(lowerStorage, "hdd") || strings.Contains(lowerStorage, "magnetic") {
		return "HDD"
	}

	if strings.Contains(lowerStorage, "instance store") || strings.Contains(lowerStorage, "local") {
		return "Local SSD"
	}

	return "Unknown"
}

// Fetch data from AWS
func fetchAWSSpotPrices() map[string]string {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	client := ec2.NewFromConfig(cfg)
	input := &ec2.DescribeSpotPriceHistoryInput{
		ProductDescriptions: []string{"Linux/UNIX"},
		StartTime:           aws.Time(time.Now()),
		MaxResults:          aws.Int32(100),
	}

	resp, err := client.DescribeSpotPriceHistory(context.TODO(), input)
	if err != nil {
		log.Fatalf("Failed to fetch AWS spot prices: %v", err)
	}

	priceMap := make(map[string]string)
	for _, price := range resp.SpotPriceHistory {
		instanceType := string(price.InstanceType)
		if _, exists := priceMap[instanceType]; !exists {
			priceMap[instanceType] = *price.SpotPrice
		}
	}
	return priceMap
}
// func fetchAWSInstances() []Instance {
// 	url := "https://api.aws.com/describe-instance-types"
// 	resp, err := http.Get(url)
// 	if err != nil {
// 		log.Println("AWS API Error:", err)
// 		return nil
// 	}
// 	defer resp.Body.Close()

// 	body, _ := ioutil.ReadAll(resp.Body)
// 	var data []Instance
// 	json.Unmarshal(body, &data)

// 	for i := range data {
// 		data[i].StorageType = mapStorageType(data[i].StorageType)
// 	}

// 	return data
// }

// Fetch data from GCP
func fetchGCPInstances() []Instance {
	 url := "https://compute.googleapis.com/compute/v1/projects/electric-totem-455409-u5/zones/us-central1-c/machineTypes"
	resp, err := http.Get(url)
	if err != nil {
		log.Println("GCP API Error:", err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var data []Instance
	json.Unmarshal(body, &data)

	for i := range data {
		data[i].StorageType = mapStorageType(data[i].StorageType)
	}

	return data
}

// Fetch data from Azure
func fetchAzureInstances() []Instance {
	url := "https://management.azure.com/subscriptions/7089df5a-2131-4f99-bc3b-4349f9b4af47/providers/Microsoft.Compute/skus?api-version=2022-03-01"
	resp, err := http.Get(url)
	if err != nil {
		log.Println("Azure API Error:", err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var data []Instance
	json.Unmarshal(body, &data)

	for i := range data {
		data[i].StorageType = mapStorageType(data[i].StorageType)
	}

	return data
}

// Save data to JSON file
func saveToJSON(data []Instance, filename string) {
	file, _ := json.MarshalIndent(data, "", " ")
	ioutil.WriteFile(filename, file, 0644)
	fmt.Printf("Data saved to %s\n", filename)
}

func main() {
	awsInstances := fetchAWSInstances()
	gcpInstances := fetchGCPInstances()
	azureInstances := fetchAzureInstances()

	allInstances := append(awsInstances, gcpInstances...)
	allInstances = append(allInstances, azureInstances...)

	saveToJSON(allInstances, "instances.json")
}
