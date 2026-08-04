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
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"
)

// ... [keep all your existing type definitions and constants] ...

type UIState struct {
	app             *tview.Application
	providerList    *tview.List
	instanceList    *tview.List
	resultsTable    *tview.Table
	statusTextView  *tview.TextView
	logView         *tview.TextView
	allInstances    []Instance
	selectedCloud   string
	selectedInstance *Instance
}

func main() {
	log.Println("Starting cloud instance comparison tool...")
	startTime := time.Now()

	// Initialize TUI
	app := tview.NewApplication()
	
	// Create UI components
	state := &UIState{
		app:            app,
		providerList:   tview.NewList(),
		instanceList:   tview.NewList(),
		resultsTable:   tview.NewTable(),
		statusTextView: tview.NewTextView(),
		logView:        tview.NewTextView(),
	}

	// Configure log output to both console and TUI
	log.SetOutput(&dualWriter{
		consoleWriter: os.Stdout,
		tuiWriter:     state.logView,
	})

	// Configure UI layout
	leftPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(createProviderPane(state), 0, 1, true).
		AddItem(createInstancePane(state), 0, 2, false)

	rightPanel := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(createResultsPane(state), 0, 3, false).
		AddItem(createLogPane(state), 0, 2, false)

	flex := tview.NewFlex().
		AddItem(leftPanel, 0, 2, true).
		AddItem(rightPanel, 0, 3, false)

	// Configure status bar
	statusBar := tview.NewFlex().
		AddItem(state.statusTextView, 0, 1, false)
	
	mainLayout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(flex, 0, 1, true).
		AddItem(statusBar, 1, 1, false)

	// Load data in background
	go func() {
		log.Println("Fetching AWS instances...")
		awsStartTime := time.Now()
		awsInstances := fetchAWSInstances()
		log.Printf("AWS fetch completed in %v, got %d instances", time.Since(awsStartTime), len(awsInstances))
		
		log.Println("Fetching GCP instances...")
		gcpStartTime := time.Now()
		gcpInstances := fetchGCPInstances("your-project-id")
		log.Printf("GCP fetch completed in %v, got %d instances", time.Since(gcpStartTime), len(gcpInstances))
		
		log.Println("Fetching Azure instances...")
		azureStartTime := time.Now()
		azureInstances := fetchAzureInstances("your-subscription-id")
		log.Printf("Azure fetch completed in %v, got %d instances", time.Since(azureStartTime), len(azureInstances))

		state.allInstances = append(append(awsInstances, gcpInstances...), azureInstances...)
		log.Printf("Combined all clouds: %d total instances", len(state.allInstances))
		
		app.QueueUpdateDraw(func() {
			state.updateStatus(fmt.Sprintf("Data loaded (%d instances)", len(state.allInstances)))
			state.populateProviders()
		})
	}()

	// Run application
	if err := app.SetRoot(mainLayout, true).EnableMouse(true).Run(); err != nil {
		log.Fatalf("Application error: %v", err)
	}
}

// ... [keep all your existing instance fetching functions exactly as they were] ...

// UI Creation Functions
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
	
	// Update details when selection changes
	state.instanceList.SetChangedFunc(func(index int, name string, secondary string, shortcut rune) {
		for _, inst := range state.allInstances {
			if inst.Name == name && inst.Provider == state.selectedCloud {
				detailsView.Clear()
				fmt.Fprintf(detailsView, "Provider: %s\n", inst.Provider)
				fmt.Fprintf(detailsView, "vCPUs: %d\n", inst.VCpus)
				fmt.Fprintf(detailsView, "Memory: %.1f GB\n", inst.MemoryGB)
				fmt.Fprintf(detailsView, "Storage: %s\n", inst.StorageType)
				fmt.Fprintf(detailsView, "Network: %s\n", inst.NetworkPerf)
				if inst.SpotPrice > 0 {
					fmt.Fprintf(detailsView, "Price: $%.4f\n", inst.SpotPrice)
				} else {
					fmt.Fprintf(detailsView, "Price: N/A\n")
				}
				break
			}
		}
	})
	
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

func createLogPane(state *UIState) *tview.Flex {
	state.logView.SetDynamicColors(true).
		SetScrollable(true).
		SetChangedFunc(func() { state.app.Draw() })
	
	state.logView.SetBorder(true).SetTitle(" Logs ")
	
	return tview.NewFlex().
		AddItem(state.logView, 0, 1, false)
}

// ... [keep all your existing helper functions] ...

// UI State Management
func (state *UIState) populateProviders() {
	state.providerList.Clear()
	state.providerList.AddItem("AWS", "", 'a', func() {
		state.selectedCloud = "AWS"
		log.Printf("Selected provider: AWS")
		state.populateInstances()
	})
	state.providerList.AddItem("GCP", "", 'g', func() {
		state.selectedCloud = "GCP"
		log.Printf("Selected provider: GCP")
		state.populateInstances()
	})
	state.providerList.AddItem("Azure", "", 'z', func() {
		state.selectedCloud = "Azure"
		log.Printf("Selected provider: Azure")
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
				log.Printf("Selected instance: %s/%s", state.selectedCloud, instance.Name)
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
	log.Printf("Found %d equivalents for %s/%s", len(equivalents), state.selectedCloud, state.selectedInstance.Name)
	
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

// dualWriter writes to both console and TUI log view
type dualWriter struct {
	consoleWriter *os.File
	tuiWriter     *tview.TextView
}

func (w *dualWriter) Write(p []byte) (n int, err error) {
	// Write to console
	n, err = w.consoleWriter.Write(p)
	if err != nil {
		return n, err
	}
	
	// Write to TUI
	w.tuiWriter.Write(p)
	
	return n, nil
}