package main

import(
	"os/exec"
	"os"
	"fmt"
	"strings"
	"encoding/json"
	"net/http"
	"bufio"
	"log"
	"encoding/csv"
	// "time"
	// "runtime"
	// shell "github.com/ipfs/go-ipfs-api"
) 

type GeoLocation struct {
	City       City       `json:"city"`
	Continent  Continent  `json:"continent"`
	Country    Country    `json:"country"`
	Location   Location   `json:"location"`
	Subdivisions []Subdivision `json:"subdivisions"`
	Traits     Traits     `json:"traits"`
}

type City struct {
	Names map[string]string `json:"names"`
}

type Continent struct {
	Names map[string]string `json:"names"`
}

type Country struct {
	Names map[string]string `json:"names"`
}

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Subdivision struct {
	Names map[string]string `json:"names"`
}

type Traits struct {
	Isp string `json:"isp"`
}

func getProvidersForCID(cid string) ([]string, error) {
	cmd := exec.Command("ipfs", "routing", "findprovs", cid)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get providers for CID %s: %v", cid, err)
	}

	lines := strings.Split(string(output), "\n")
	var providers []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			providers = append(providers, line)
		}
	}
	return providers, nil
}

func getIPForPeer(peerID string) (string, error) {
	cmd := exec.Command("ipfs", "id", peerID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get ID for peer %s: %v", peerID, err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("failed to parse IP for peer %s: %v", peerID, err)
	}

	addresses, ok := result["Addresses"].([]interface{})
	if !ok || len(addresses) == 0 {
		return "", fmt.Errorf("no addresses found for peer %s", peerID)
	}

	for _, addr := range addresses {
		addrStr, ok := addr.(string)
		if ok && strings.HasPrefix(addrStr, "/ip4/") {
			parts := strings.Split(addrStr, "/")
			if len(parts) >= 3 {
				return parts[2], nil
			}
		}
	}
	return "", fmt.Errorf("no valid IP address found for peer %s", peerID)
}

func getGeoLocation(ip, token string) (*GeoLocation, error) {
	url := fmt.Sprintf("https://api.findip.net/%s/?token=%s", ip, token)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get geolocation for IP %s: %v", ip, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from geolocation API", resp.StatusCode)
	}

	var geoInfo GeoLocation
	if err := json.NewDecoder(resp.Body).Decode(&geoInfo); err != nil {
		return nil, fmt.Errorf("failed to decode geolocation response for IP %s: %v", ip, err)
	}

	return &geoInfo, nil
}

/*
func checkIPFSDaemon() bool {
	sh := shell.NewShell("localhost:5001")
	_, err := sh.ID()
	if err != nil {
		log.Printf("IPFS daemon is not running: %s", err)
		return false
	}
	log.Println("IPFS daemon is running.")
	return true
}

func restartIPFSDaemon() error {
	log.Println("Restarting IPFS daemon...")

	isRunning, err := isIPFSDaemonRunning()
	if err != nil {
		log.Printf("Failed to check IPFS daemon status: %v", err)
		isRunning = false
	}

	if isRunning {
		var stopCmd *exec.Cmd

		if runtime.GOOS == "windows" {
			stopCmd = exec.Command("taskkill", "/F", "/IM", "ipfs.exe")
		} else {
			stopCmd = exec.Command("pkill", "-f", "ipfs daemon")
		}

		if err := stopCmd.Run(); err != nil {
			log.Printf("Failed to stop IPFS daemon (it might not be running): %v", err)
		}
	}

	var startCmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// startCmd = exec.Command("cmd", "/C", "start", "ipfs", "daemon")
		startCmd = exec.Command("cmd", "/C", "start", "/b", "ipfs", "daemon")
	} else {
		startCmd = exec.Command("nohup", "ipfs", "daemon", "&")
	}

	if err := startCmd.Start(); err != nil {
		return fmt.Errorf("failed to start IPFS daemon: %v", err)
	}

	log.Println("IPFS daemon restarted successfully.")
	time.Sleep(10 * time.Second)

	// Verify if the IPFS daemon has started correctly
	retries := 3
	for i := 0; i < retries; i++ {
		if checkIPFSDaemon() {
			log.Println("IPFS daemon is running.")
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("IPFS daemon failed to start after %d retries", retries)
}

func isIPFSDaemonRunning() (bool, error) {
	var checkCmd *exec.Cmd

	if runtime.GOOS == "windows" {
		checkCmd = exec.Command("tasklist", "/FI", "IMAGENAME eq ipfs.exe")
	} else {
		checkCmd = exec.Command("pgrep", "-f", "ipfs daemon")
	}

	output, err := checkCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to check IPFS daemon process: %v", err)
	}

	return strings.TrimSpace(string(output)) != "", nil
}*/

func gatherProviderInfo(activeLinksFile, outputFile, token string) error {
	file, err := os.Open(activeLinksFile)
	if err != nil {
		return fmt.Errorf("failed to open active IPFS links file: %v", err)
	}
	defer file.Close()

	// Create output CSV file
	outFile, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create provider info CSV file: %v", err)
	}
	defer outFile.Close()
	csvWriter := csv.NewWriter(outFile)
	defer csvWriter.Flush()

	header := []string{"CID", "ProviderID", "IP", "City", "Continent", "Country", "Latitude", "Longitude", "ISP"}
	if err := csvWriter.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %v", err)
	}

	scanner := bufio.NewScanner(file)
	// Skip the header line
	scanner.Scan()

	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Split(line, ",")
		cid := fields[0]
		if cid == "" {
			continue
		}

		/*
		if !checkIPFSDaemon() {
			log.Println("[I] IPFS daemon is not running. Restarting...")
			if err:= restartIPFSDaemon(); err !=nil {
				log.Fatalf("[I] Failed to restart IPFS daemon: %s", err)
			}
		}
		*/

		providers, err := getProvidersForCID(cid)
		if err != nil {
			log.Printf("Failed to get providers for CID %s: %v", cid, err)
			continue
		}

		for _, provider := range providers {
			providerID := provider
			ip, err := getIPForPeer(providerID)
			if err != nil {
				log.Printf("Failed to get IP for peer %s: %v", providerID, err)
				continue
			}

			geoInfo, err := getGeoLocation(ip, token)
			if err != nil {
				log.Printf("Failed to get geolocation for IP %s: %v", ip, err)
				continue
			}

			record := []string{cid, providerID, ip, geoInfo.City.Names["en"], geoInfo.Continent.Names["en"], geoInfo.Country.Names["en"], fmt.Sprintf("%f", geoInfo.Location.Latitude), fmt.Sprintf("%f", geoInfo.Location.Longitude), geoInfo.Traits.Isp}
			if err := csvWriter.Write(record); err != nil {
				log.Printf("Failed to write record to CSV: %v", err)
			}
			csvWriter.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading active IPFS links file: %v", err)
	}

	log.Println("Provider info gathering completed successfully.")
	return nil
}
