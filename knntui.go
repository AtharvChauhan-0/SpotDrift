package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type SpecMapEntry struct {
	ID          string  `json:"id"`
	Provider    string  `json:"provider"`
	Name        string  `json:"name"`
	Region      string  `json:"region"`
	VCpus       int     `json:"vcpus"`
	MemoryGB    float64 `json:"memoryGB"`
	SpotPrice   float64 `json:"spotPrice"`
	InstanceType string `json:"instanceType"`
}

type model struct {
	entries        []SpecMapEntry
	inputs         []textinput.Model
	cursor         int
	results        []SpecMapEntry
	selectedResult int
	searching      bool
}

var (
	normalizedVCpus    float64
	normalizedMemoryGB float64
	normalizedPrice    float64

	maxVCpus    = 0
	maxMemoryGB = 0.0
	maxPrice    = 0.0

	inputStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	resultStyle = lipgloss.NewStyle().MarginLeft(2)
)

func initialModel(entries []SpecMapEntry) model {
	inputs := make([]textinput.Model, 3)
	inputs[0] = textinput.New()
	inputs[0].Placeholder = "vCPUs"
	inputs[0].Focus()

	inputs[1] = textinput.New()
	inputs[1].Placeholder = "Memory (GB)"

	inputs[2] = textinput.New()
	inputs[2].Placeholder = "Max Price"

	// Find max values for normalization
	for _, entry := range entries {
		if entry.VCpus > maxVCpus {
			maxVCpus = entry.VCpus
		}
		if entry.MemoryGB > maxMemoryGB {
			maxMemoryGB = entry.MemoryGB
		}
		if entry.SpotPrice > maxPrice {
			maxPrice = entry.SpotPrice
		}
	}

	return model{
		entries: entries,
		inputs:  inputs,
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "enter":
			if m.cursor < len(m.inputs)-1 {
				m.inputs[m.cursor].Blur()
				m.cursor++
				m.inputs[m.cursor].Focus()
				return m, nil
			} else {
				m.searching = true
				m.results = knnSearch(m.entries, m.inputs)
				return m, tea.Quit
			}
		case "up", "down":
			if len(m.results) > 0 {
				if msg.String() == "up" && m.selectedResult > 0 {
					m.selectedResult--
				} else if msg.String() == "down" && m.selectedResult < len(m.results)-1 {
					m.selectedResult++
				}
			}
		}
	}

	// Update inputs
	cmd := m.updateInputs(msg)
	return m, cmd
}

func (m *model) updateInputs(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd

	for i := range m.inputs {
		m.inputs[i], _ = m.inputs[i].Update(msg)
	}

	return tea.Batch(cmds...)
}

func (m model) View() string {
	if m.searching {
		return m.viewResults()
	}

	return fmt.Sprintf(
		`Specify your desired VM characteristics:

%s
%s
%s

%s
`,
		inputStyle.Render(m.inputs[0].View()),
		inputStyle.Render(m.inputs[1].View()),
		inputStyle.Render(m.inputs[2].View()),
		errorStyle.Render("(press enter to search, q to quit)"),
	)
}

func (m model) viewResults() string {
    if len(m.results) == 0 {
        return errorStyle.Render("No matching instances found")
    }

    var s string
    for i, result := range m.results {
        selected := "  "
        if i == m.selectedResult {
            selected = "» "  // Ensure visible selection marker
        }
        s += fmt.Sprintf("%s%s/%s - %d vCPUs, %.1fGB RAM, %s, $%.4f/hr\n",
            selected,
            result.Provider,
            result.Name,
            result.VCpus,
            result.MemoryGB,
            result.Region,  // Added region information
            result.SpotPrice,
        )
    }
    return resultStyle.Render(s)
}

func knnSearch(entries []SpecMapEntry, inputs []textinput.Model) []SpecMapEntry {
	var targetVCpus, targetMemoryGB, targetPrice float64
	fmt.Sscan(inputs[0].Value(), &targetVCpus)
	fmt.Sscan(inputs[1].Value(), &targetMemoryGB)
	fmt.Sscan(inputs[2].Value(), &targetPrice)

	// Normalize target features
	normVCpus := normalize(float64(targetVCpus), 0, float64(maxVCpus))
	normMemory := normalize(targetMemoryGB, 0, maxMemoryGB)
	normPrice := normalize(targetPrice, 0, maxPrice)

	type scoredEntry struct {
		entry SpecMapEntry
		score float64
	}

	var scoredEntries []scoredEntry

	for _, e := range entries {
		// Normalize entry features
		eVCpus := normalize(float64(e.VCpus), 0, float64(maxVCpus))
		eMemory := normalize(e.MemoryGB, 0, maxMemoryGB)
		ePrice := normalize(e.SpotPrice, 0, maxPrice)

		// Calculate Euclidean distance
		distance := math.Sqrt(
			math.Pow(eVCpus-normVCpus, 2) +
				math.Pow(eMemory-normMemory, 2) +
				math.Pow(ePrice-normPrice, 2))

		scoredEntries = append(scoredEntries, scoredEntry{e, distance})
	}

	// Sort by ascending distance
	sort.Slice(scoredEntries, func(i, j int) bool {
		return scoredEntries[i].score < scoredEntries[j].score
	})

	// Return top 5 matches
	var results []SpecMapEntry
	for i := 0; i < 5 && i < len(scoredEntries); i++ {
		results = append(results, scoredEntries[i].entry)
	}

	return results
}

func normalize(value, min, max float64) float64 {
	if max == min {
		return 0.5
	}
	return (value - min) / (max - min)
}

func loadSpecMap(path string) []SpecMapEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}

	var entries map[string]SpecMapEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Fatal(err)
	}

	var result []SpecMapEntry
	for _, entry := range entries {
		result = append(result, entry)
	}

	return result
}

func main() {
	entries := loadSpecMap("spec_map.json")
	p := tea.NewProgram(initialModel(entries))

	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}