package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	cid "github.com/ipfs/go-cid"
	multibase "github.com/multiformats/go-multibase"
)

// date: 09.06.2024
// added github.com/ipfs/go-cid package
// go install github.com/ipfs/go-cid@latest

var (
	ipfsGateways []string
	gatewayLock  sync.Mutex
)

func readGatewaysFromFile(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var gateways []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		gateways = append(gateways, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return gateways, nil
}

func addNewGateway(newGateway string) {
	gatewayLock.Lock()
	defer gatewayLock.Unlock()

	for _, gateway := range ipfsGateways {
		if gateway == newGateway {
			return
		}
	}

	ipfsGateways = append(ipfsGateways, newGateway)


	file, err := os.OpenFile("ipfs_gateways.txt", os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open gateways file: %s", err)
		return
	}
	defer file.Close()

	if _, err := file.WriteString(newGateway + "\n"); err != nil {
		log.Printf("Failed to write new gateway to file: %s", err)
	}
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

func downloadFile(url string, filePathStr string) error {
	dir := filepath.Dir(filePathStr)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	out, err := os.Create(filePathStr)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad Status: %s", resp.Status)
	}

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func findIPFSLinks(filePathStr string, outputFilePath string) error {
	file, err := os.Open(filePathStr)
	if err != nil {
		return err
	}
	defer file.Close()

	outputFile, err := os.Create(outputFilePath)
	if err != nil {
		return err
	}
	defer outputFile.Close()

	var ipfsLinks []string

	// Regular expression to match both types of IPFS links
	ipfsRegex := regexp.MustCompile(`(ipfs://\S+|https?://[^\s]+/ipfs/[a-zA-Z0-9]+[^\s]*)`)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		matches := ipfsRegex.FindAllString(line, -1)
		if matches != nil {
			ipfsLinks = append(ipfsLinks, matches...)
			for _, link := range matches {
				if _, err = outputFile.WriteString(link + "\n"); err != nil {
					return err
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func fetchAndParseDenyList(url string) (map[string]bool, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	denyList := make(map[string]bool)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "//") {
			denyList[line[2:]] = true
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return denyList, nil
}

func checkIPFSLinks(filePathStr string, gateways []string, filePathActive string, filePathActiveTxt string, denyListURL string) {
	//fetching deny list
	denyList, err := fetchAndParseDenyList(denyListURL)
	if err != nil {
		log.Fatalf("Failed to fetch deny list: %s", err)
	}

	file, err := os.Open(filePathStr)
	if err != nil {
		log.Fatalf("Failed to open file: %s", err)
	}
	defer file.Close()

	fileActiveTxt, err := os.Create(filePathActiveTxt)
	if err != nil {
		log.Fatalf("Failed to create text file: %s", err)
	}
	defer fileActiveTxt.Close()

	fileActive, err := os.Create(filePathActive)
	if err != nil {
		log.Fatalf("Failed to create CSV file: %s", err)
	}
	defer fileActive.Close()
	csvWriter := csv.NewWriter(fileActive)
	defer csvWriter.Flush()

	// Write CSV header
	header := append([]string{"CID", "Blocked"}, gateways...)
	if err := csvWriter.Write(header); err != nil {
		log.Fatalf("Failed to write CSV header: %s", err)
	}

	ipfsRegex := regexp.MustCompile(`(ipfs://\S+|https?://[^\s]+/ipfs/[a-zA-Z0-9]+[^\s]*)`)
	scanner := bufio.NewScanner(file)

	var wg sync.WaitGroup
	linkChannel := make(chan string)

	processedCIDs := make(map[string]bool)
	var resultsLock sync.Mutex

	// Launch goroutines to send requests
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for link := range linkChannel {
				cidStr := extractCID(link)

				resultsLock.Lock()
				if processedCIDs[cidStr] {
					resultsLock.Unlock()
					continue
				}
				processedCIDs[cidStr] = true
				resultsLock.Unlock()

				statuses := make([]string, len(gateways))
				anyAvailable := false
				for i, gateway := range gateways {
					url := gateway + cidStr
					status, err := sendReq(url)
					if err != nil {
						log.Printf("[XX] Failed to fetch %s: %s", url, err)
						statuses[i] = "-"
						continue
					}

					if status == http.StatusOK {
						log.Printf("[++] Content is available at %s", url)
						statuses[i] = "+"
						anyAvailable = true

						resultsLock.Lock()
						if _, err := fileActiveTxt.WriteString(url + "\n"); err != nil {
							log.Printf("Failed to write active link to the text file: %s", err)
						}
						resultsLock.Unlock()
					} else {
						log.Printf("[!!] Content not found at %s", url)
						statuses[i] = "-"
					}
				}

				//Checking for additional/new gateways in the found ipfs links and adding them to the ipfsGateways list
				if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
					parts := strings.Split(link, "/ipfs/")
					if len(parts) > 1 {
						baseURL := parts[0] + "/ipfs/"
						addNewGateway(baseURL)
					}
				}

				//Converting CID to hash & checking with Bad Bits Denylist
				hashedCID, err := convertCIDToHash(cidStr)
				if err != nil {
					log.Printf("Failed to convert CID to hash: %s", err)
					continue
				}
				blocked := "no"
				if denyList[hashedCID] {
					blocked = "yes"
				}


				if anyAvailable {
					resultRow := append([]string{cidStr, blocked}, statuses...)
					func() {
						resultsLock.Lock()
						defer resultsLock.Unlock()
						if err := csvWriter.Write(resultRow); err != nil {
							log.Printf("Failed to write row to the CSV file: %s", err)
						}
						csvWriter.Flush()
					}()
				}

			}
		}()
	}

	// Read links and send them to the channel
	for scanner.Scan() {
		line := scanner.Text()
		matches := ipfsRegex.FindAllString(line, -1)
		for _, match := range matches {
			linkChannel <- match
		}
	}
	close(linkChannel)

	wg.Wait()

	if err := scanner.Err(); err != nil {
		log.Printf("Error scanning file: %s", err)
	}
}

func extractCID(link string) string {
	if strings.HasPrefix(link, "ipfs://") {
		return strings.TrimPrefix(link, "ipfs://")
	}

	parts := strings.Split(link, "/ipfs/")
	if len(parts) < 2 {
		return ""
	}
	cidPart := parts[1]
	if pos := strings.IndexAny(cidPart, " ?#"); pos >= 0 {
		cidPart = cidPart[:pos]
	}
	return cidPart
}

func sendReq(url string) (int, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func convertCIDToHash(cidStr string) (string, error) {
	// Parse the CID
	c, err := cid.Decode(cidStr)
	if err != nil {
		return "", err
	}

	// Convert to CIDv1 and encode as base32
	// c = c.ToV1() -- this is not supported in the newer version
	c = cid.NewCidV1(c.Type(), c.Hash())

	// base32CID := c.StringOfBase(cid.Base32)
	// base32CID := c.String()
	base32CID, err := c.StringOfBase(multibase.Base32)
	if err != nil {
		return "", err
	}
	

	// Apply SHA-256 and encode as hex
	hash := sha256.Sum256([]byte(base32CID))
	return fmt.Sprintf("%x", hash), nil
}


func main() {
	var extract bool
	flag.BoolVar(&extract, "e", false, "extract results live in CSV format")
	flag.Parse()

	fileURL := "https://raw.githubusercontent.com/mitchellkrogza/Phishing.Database/master/ALL-phishing-links.txt"
	filePath := "./phishing_db/ALL-phishing-links.txt"
	filePathIPFSLinks := "./phishing_db/found-ipfs-phishing-links.txt"

	date := time.Now().Format("20060102")
	filePathActiveIPFSLinks := fmt.Sprintf("./collected_data/%s_found-ipfs-phishing-links_ACTIVE.csv", date)
	filePathActiveTxt := fmt.Sprintf("./collected_data/%s_found-ipfs-phishing-links_ACTIVE.txt", date)

	//error handling for the collected_data directory
	if _, err := os.Stat("./collected_data"); os.IsNotExist(err) {
		if err := os.Mkdir("./collected_data", 0755); err != nil {
			log.Fatalf("Failed to create collected_data directory: %s", err)
		}
	}


	var err error
	ipfsGateways, err = readGatewaysFromFile("./src/ipfs_gateways.txt")
	if err != nil {
		log.Fatalf("Failed to read gateways from file: %s", err)
	}

	if err := downloadFile(fileURL, filePath); err != nil {
		log.Fatalf("[!!] error downloading db: %s", err)
	}

	log.Println("[++] DB downloaded successfully")

	if err := findIPFSLinks(filePath, filePathIPFSLinks); err != nil {
		log.Fatalf("Failed to find ipfs links: %s", err)
	}

	log.Println("[!!] ipfs links found and saved successfully.")

	denyListURL := "https://badbits.dwebops.pub/badbits.deny"

	checkIPFSLinks(filePathIPFSLinks, ipfsGateways, filePathActiveIPFSLinks, filePathActiveTxt, denyListURL)
}
