package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
)

type Instance struct {
	Name             string  `json:"name"`
	VCpus            int     `json:"vcpus"`
	MemoryGB         float64 `json:"memory_gb"`
	StorageType      string  `json:"storage_type"`
	NetworkPerformance string `json:"network_performance"`
}

// Calculate similarity based on resource comparison
func calculateSimilarity(a, b Instance) float64 {
	vcpuDiff := math.Abs(float64(a.VCpus - b.VCpus))
	memoryDiff := math.Abs(a.MemoryGB - b.MemoryGB)
	
	// Prioritize matching storage type
	storageMatch := 0.0
	if a.StorageType == b.StorageType {
		storageMatch = 1.0
	}
	
	// Calculate a normalized similarity score
	return 1.0 / (1.0 + vcpuDiff + memoryDiff) + storageMatch
}

// Find the best match for an input instance
func findBestMatch(input Instance, instances []Instance) Instance {
	bestMatch := instances[0]
	bestScore := 0.0

	for _, instance := range instances {
		score := calculateSimilarity(input, instance)
		if score > bestScore {
			bestScore = score
			bestMatch = instance
		}
	}

	return bestMatch
}

func loadInstances(filename string) ([]Instance, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var instances []Instance
	err = json.Unmarshal(data, &instances)
	if err != nil {
		return nil, err
	}

	return instances, nil
}

func main() {
	awsInstances, err := loadInstances("aws_instances.json")
	if err != nil {
		fmt.Println("Error reading AWS JSON file:", err)
		return
	}

gcpInstances, err := loadInstances("gcp_instances.json")
	if err != nil {
		fmt.Println("Error reading GCP JSON file:", err)
		return
	}

azureInstances, err := loadInstances("azure_instances.json")
	if err != nil {
		fmt.Println("Error reading Azure JSON file:", err)
		return
	}

	// Combine all instances
	allInstances := append(awsInstances, gcpInstances...)
	allInstances = append(allInstances, azureInstances...)

	// Example input instance (e.g., AWS t2.micro)
	inputInstance := Instance{Name: "t2.micro", VCpus: 1, MemoryGB: 1.0, StorageType: "General-Purpose SSD"}

	// Find the best match
	bestMatch := findBestMatch(inputInstance, allInstances)

	fmt.Printf("Best match for %s: %s\n", inputInstance.Name, bestMatch.Name)
}
