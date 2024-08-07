package main

import (
	"bufio"
	"os/exec"
	// "bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	// "encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	// "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"runtime"

	// "github.com/ipfs/go-cid"
	shell "github.com/ipfs/go-ipfs-api"
)

// date: 09.06.2024
// added github.com/ipfs/go-cid package
// go install github.com/ipfs/go-cid@latest

var (
	ipfsGateways []string
	gatewayLock  sync.Mutex
	httpClient = &http.Client{
		Timeout: 1 * time.Minute,
	}
	apiToken string
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


	file, err := os.OpenFile("./src/ipfs_gateways.txt", os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open gateways file: %s", err)
		return
	}
	defer file.Close()

	if _, err := file.WriteString(newGateway + "\n"); err != nil {
		log.Printf("Failed to write new gateway to file: %s", err)
	}
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

	// var ipfsLinks []string

	// Regular expression to match both types of IPFS links
	ipfsRegex := regexp.MustCompile(`(ipfs://\S+|https?://[^\s]+/ipfs/[a-zA-Z0-9]+[^\s]*)`)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		matches := ipfsRegex.FindAllString(line, -1)
		// if matches != nil {
			// ipfsLinks = append(ipfsLinks, matches...)
			for _, link := range matches {
				if _, err = outputFile.WriteString(link + "\n"); err != nil {
					return err
				}
			}
		// }
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func hashData(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func getStatusMessage(err error) string {
	if err == nil {
		return "Success"
	}
	return err.Error()
}

func processCIDs(linkChannel <-chan string, gateways []string, csvWriter, matchContentWriter *csv.Writer, responseWriter *csv.Writer, fileActiveTxt *os.File, resultsLock *sync.Mutex, processedCIDs map[string]bool, extractFromURL bool) {
	for link := range linkChannel {
		var cidStr string
		if extractFromURL {
			cidStr = extractCID(link)
		} else {
			cidStr = link
		}

		resultsLock.Lock()
		if processedCIDs[cidStr] {
			resultsLock.Unlock()
			continue
		}
		processedCIDs[cidStr] = true
		resultsLock.Unlock()

		statuses := make([]string, len(gateways))
		matches := make([]string, len(gateways))
		anyAvailable := false
		matchWithIPFS := false

		// ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		
		var ipfsContent []byte
		var err error
		for {
			if !checkIPFSDaemon() {
				log.Println("[I] IPFS daemon is not running. Restarting...")
				if err:= restartIPFSDaemon(); err !=nil {
					log.Fatalf("[I] Failed to restart IPFS daemon: %s", err)
				}
			}

			ipfsContent, err = fetchContentFromIPFSWithTimeout(cidStr, 1*time.Minute)
			if err != nil && (strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "EOF")) {
				log.Println("[I] IPFS request timed out. Restarting IPFS daemon...")
				if err := restartIPFSDaemon(); err != nil {
					log.Fatalf("[I] Failed to restart IPFS daemon: %s", err)
				}
				continue
			}
			break
		}
		
		if err != nil {
			log.Printf("Failed to fetch content from IPFS for CID %s: %s", cidStr, err)
			ipfsContent = nil
		}

		ipfsHash := hashData(ipfsContent)

		responseStatuses := []string{cidStr, "IPFS response: " + getStatusMessage(err)}

		for i, gateway := range gateways {
			// gatewayCtx, gatewayCancel := context.WithTimeout(context.Background(), 1*time.Minute)
			url := gateway + cidStr
			// status, err := sendReqWithContext(gatewayCtx, url)
			status, err := sendReq(url)
			responseStatus := getStatusMessage(err)
			// gatewayCancel()
			if err != nil {
				log.Printf("[XX] Failed to fetch %s: %s", url, err)
				statuses[i] = "-"
				matches[i] = "-"
				responseStatuses = append(responseStatuses, responseStatus)
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

				//fetching content from HTTP
				httpContent, err := fetchContentFromHTTP(url)
				if err != nil {
					log.Printf("Failed to fetch content from HTTP for URL %s: %s", url, err)
					matches[i] = "-"
					responseStatuses = append(responseStatuses, "HTTP Response: " + getStatusMessage(err))
					continue
				}

				//Comparing hashes
				// if ipfsContent != nil {
				httpHash := hashData(httpContent)
				if ipfsHash == httpHash {
					matches[i] = "+"
					matchWithIPFS = true
				} else {
					matches[i] = "-"
				}
				// } else {
					// matches[i] = "-"
				// }

			} else if status == http.StatusGone || status == http.StatusUnavailableForLegalReasons {
				log.Printf("[!!!] Content blocked at %s with status %d", url, status)
				statuses[i] = "x"
				matches[i] = "x"
				responseStatuses = append(responseStatuses, "Blocked (HTTP STATUS 410 or 451)")
			} else {
				log.Printf("[!!] Content not found at %s", url)
				statuses[i] = "-"
				matches[i] = "-"
				responseStatuses = append(responseStatuses, "Not Found")
			}
		}

		//Checking for additional/new gateways in the found IPFS links and adding to the IPFS gateways list
		if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
			parts := strings.Split(link, "/ipfs/")
			if len(parts) > 1 {
				baseURL := parts[0] + "/ipfs/"
				addNewGateway(baseURL)
			}
		}


		if anyAvailable {
			resultRow := append([]string{cidStr, "+"}, statuses...)
			func() {
				resultsLock.Lock()
				defer resultsLock.Unlock()
				if err := csvWriter.Write(resultRow); err != nil {
					log.Printf("Failed to write row to the CSV file: %s", err)
				}
				csvWriter.Flush()
			}()
		} else {
			resultRow := append([]string{cidStr, "-"}, statuses...)
			func() {
				resultsLock.Lock()
				defer resultsLock.Unlock()
				if err := csvWriter.Write(resultRow); err != nil {
					log.Printf("Failed to write row to the CSV file: %s", err)
				}
				csvWriter.Flush()
			}()
		}


		if matchWithIPFS {
			matchContentRow := append([]string{cidStr}, matches...)
			func() {
				resultsLock.Lock()
				defer resultsLock.Unlock()
				if err := matchContentWriter.Write(matchContentRow); err != nil {
					log.Printf("Failed to write row to the match content CSV file: %s", err)
				}
				matchContentWriter.Flush()
			}()
		}

		// writing response statuses to CSV
		func()  {
			resultsLock.Lock()
			defer resultsLock.Unlock()
			if err := responseWriter.Write(responseStatuses); err != nil {
				log.Printf("Failed to write row to the response CSV file: %s", err)
			}
			responseWriter.Flush()
		}()
	}
}

// func checkIPFSLinks(filePathStr string, gateways []string, filePathActive string, filePathActiveTxt string, phishingCIDsFilePath string) {
func checkIPFSLinks(filePathStr string, gateways []string, filePathActive string, filePathActiveTxt string) {

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

	date := time.Now().Format("20060102")
	counter := 1
	var filePathMatchContent, filePathResponses string
	for {
		filePathMatchContent = fmt.Sprintf("./collected_data/%s_matchcontent_%d.csv", date, counter)
		filePathResponses = fmt.Sprintf("./collected_data/%s_responses_%d.csv", date, counter)
		if _, err := os.Stat(filePathMatchContent); os.IsNotExist(err) {
			break
		}
		// if _, err := os.Stat(filePathResponses); os.IsNotExist(err) {
		// 	break
		// }
		counter++
	}

	fileMatchContent, err := os.Create(filePathMatchContent)
	if err != nil {
		log.Fatalf("Failed to create match content CSV file: %s", err)
	}
	defer fileMatchContent.Close()
	matchContentWriter := csv.NewWriter(fileMatchContent)
	defer matchContentWriter.Flush()

	fileResponses, err := os.Create(filePathResponses)
	if err != nil {
		log.Fatalf("Failed to create responses CSV file: %s", err)
	}
	defer fileResponses.Close()
	responseWriter := csv.NewWriter(fileResponses)
	defer responseWriter.Flush()

	// Writes CSV headers
	header := append([]string{"CID", "accessibleOnIPFS"}, gateways...)
	if err := csvWriter.Write(header); err != nil {
		log.Fatalf("Failed to write CSV header: %s", err)
	}
	matchContentHeader := append([]string{"CID"}, gateways...)
	if err := matchContentWriter.Write(matchContentHeader); err != nil {
		log.Fatalf("Failed to write match content CSV header: %s", err)
	}
	responseHeader := append([]string{"CID", "IPFS Response"}, gateways...)
	if err:= responseWriter.Write(responseHeader); err != nil {
		log.Fatalf("Failed to write responses CSV header: %s", err)
	}

	
	// scanner := bufio.NewScanner(file)

	var wg sync.WaitGroup
	processedCIDs := make(map[string]bool)
	var resultsLock sync.Mutex

	// linkChannel = make(chan string)

	// processPhishingCIDs := func() {
	// 	defer wg.Done()

	// 	// Read CIDs from the 'phishing_cids.csv' file and send them to the channel
	// 	phishingCIDsFile, err := os.Open(phishingCIDsFilePath)
	// 	if err != nil {
	// 		log.Fatalf("Failed to open phishing CIDs file: %s", err)
	// 	}
	// 	defer phishingCIDsFile.Close()
	// 	phishingCIDsScanner := bufio.NewScanner(phishingCIDsFile)
	// 	linkChannel := make(chan string)

	// 	//Launching another goroutines
	// 	for i := 0; i < 3; i++ {
	// 		wg.Add(1)
	// 		go func() {
	// 			defer wg.Done()
	// 			processCIDs(linkChannel, gateways, csvWriter, matchContentWriter, responseWriter, fileActiveTxt, &resultsLock, processedCIDs, false)
	// 		}()
	// 	}

	// 	for phishingCIDsScanner.Scan() {
	// 		cid := phishingCIDsScanner.Text()
	// 		linkChannel <- cid
	// 	}
	// 	if err := phishingCIDsScanner.Err(); err != nil {
	// 		log.Printf("Error scanning phishing CIDs file: %s", err)
	// 	}

	// 	close(linkChannel)
	// }

	processFoundIPFSLinks := func() {
		defer wg.Done()

		file, err := os.Open(filePathStr)
		if err != nil {
			log.Fatalf("Failed to open file: %s", err)
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		linkChannel := make(chan string)

		//Launching goroutines to process CIDs from 'found-ipfs-phishing-links.txt'
		for i := 0; i< 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				processCIDs(linkChannel, gateways, csvWriter, matchContentWriter, responseWriter, fileActiveTxt, &resultsLock, processedCIDs, true)
			}()
		}

		// reading links and send to channel
		for scanner.Scan() {
			line := scanner.Text()
			ipfsRegex := regexp.MustCompile(`(ipfs://\S+|https?://[^\s]+/ipfs/[a-zA-Z0-9]+[^\s]*)`)
			matches := ipfsRegex.FindAllString(line, -1)
			for _, match := range matches {
				linkChannel <- match
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("Error scanning found-ipfs-phishing-links.txt: %s", err)
		}

		close(linkChannel)
	}
	
	// wg.Add(1)
	// go processPhishingCIDs()

	// wg.Wait()
	
	wg.Add(1)
	go processFoundIPFSLinks()
	wg.Wait()
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

func fetchContentFromHTTP(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func fetchContentFromIPFSWithTimeout(cidStr string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	sh := shell.NewShell("localhost:5001")
	rc, err := sh.Cat(cidStr)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	done := make(chan struct{})
	var data []byte
	go func() {
		data, err = io.ReadAll(rc)
		close(done)
	}()

	select {
	case <-done:
		return data, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func initLogger()  {
	date := time.Now().Format("20060102")
	counter := 1

	var logFilePath string
	for {
		logFilePath = fmt.Sprintf("./log/%s_%d.log", date, counter)
		if _, err := os.Stat(logFilePath); os.IsNotExist(err) {
			break
		}
		counter++
	}

	if _, err := os.Stat("./log"); os.IsNotExist(err) {
		if err := os.Mkdir("./log", 0755); err != nil {
			log.Fatalf("Failed to create log directory: %s", err)
		}
	}

	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to create log file: %s", err)
	}

	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
}

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
}

// func continuouslyRestartIPFSDaemon() {
// 	for {
// 		time.Sleep(1 * time.Minute)
// 		log.Println("[I] Restarting IPFS daemon...")
// 		if err := restartIPFSDaemon(); err != nil {
// 			log.Printf("[I] Failed to restart IPFS daemon: %s", err)
// 		} else {
// 			log.Println("IPFS daemon restarted successfully.")
// 		}
// 	}
// }


func main() {
	initLogger()

	// go continuouslyRestartIPFSDaemon()

	apiTokenBytes, err := os.ReadFile("./src/api_token.txt")
	if err != nil {
		log.Fatalf("Failed to read API token: %s", err)
	}
	apiToken = strings.TrimSpace(string(apiTokenBytes))

	var extract bool
	flag.BoolVar(&extract, "e", false, "extract results live in CSV format")
	var providerInfoFile string
	flag.StringVar(&providerInfoFile, "providerInfoFile", "", "file name for provider info")
	flag.Parse()

	// if !checkIPFSDaemon() {
	// 	log.Println("IPFS daemon is not running. Restarting...")
	// 	for {
	// 		if err := restartIPFSDaemon(); err != nil {
	// 			log.Printf("Failed to restart IPFS daemon: %s", err)
	// 			time.Sleep(5 * time.Second) // Wait before retrying
	// 		} else {
	// 			log.Println("IPFS daemon restarted successfully.")
	// 			break
	// 		}
	// 	}
	// }

	fileURL := "https://raw.githubusercontent.com/mitchellkrogza/Phishing.Database/master/ALL-phishing-links.txt"
	filePath := "./phishing_db/ALL-phishing-links.txt"
	filePathIPFSLinks := "./phishing_db/found-ipfs-phishing-links.txt"
	// phishingCIDsFile := "./src/phishing_cids.csv"

	//error handling for the collected_data directory
	if _, err := os.Stat("./collected_data"); os.IsNotExist(err) {
		if err := os.Mkdir("./collected_data", 0755); err != nil {
			log.Fatalf("Failed to create collected_data directory: %s", err)
		}
	}

	
	date := time.Now().Format("20060102")
	counter := 1
	// var filePathActiveIPFSLinks, 
	var filePathActiveIPFSLinks, filePathActiveTxt, filePathProviderInfo string
	for {
		filePathActiveIPFSLinks = fmt.Sprintf("./collected_data/%s_found-ipfs-phishing-links_ACTIVE_%d.csv", date, counter)
		filePathActiveTxt = fmt.Sprintf("./collected_data/%s_found-ipfs-phishing-links_ACTIVE_%d.txt", date, counter)
		// filePathResponses = fmt.Sprintf("./collected_data/%s_responses_%d.csv", date, counter)
		filePathProviderInfo = fmt.Sprintf("./collected_data/%s_provider_info_%d.csv", date, counter)
		if _, err := os.Stat(filePathProviderInfo); os.IsNotExist(err) {
			break
		}
		counter++	 
	}

	// var err error
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

	// If providerInfoFile flag is provided, gather provider info for that specific file
	if providerInfoFile != "" {
		if err := gatherProviderInfo(providerInfoFile, filePathProviderInfo, apiToken); err != nil {
			log.Fatalf("Failed to gather provider info: %s", err)
		}
		return
	}
	
	download_content()

	// checkIPFSLinks(filePathIPFSLinks, ipfsGateways, filePathActiveIPFSLinks, filePathActiveTxt, phishingCIDsFile)
	checkIPFSLinks(filePathIPFSLinks, ipfsGateways, filePathActiveIPFSLinks, filePathActiveTxt)

	if err := gatherProviderInfo(filePathActiveIPFSLinks, filePathProviderInfo, apiToken); err != nil {
		log.Fatalf("Failed to gather provider info: %s", err)
	}

	// checkBadBits()
}
