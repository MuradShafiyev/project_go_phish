package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	
	shell "github.com/ipfs/go-ipfs-api"
	"github.com/ipfs/go-cid"
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

func processCIDs(linkChannel <-chan string, gateways []string, csvWriter, matchContentWriter *csv.Writer, fileActiveTxt *os.File, resultsLock *sync.Mutex, processedCIDs map[string]bool) {
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
		matches := make([]string, len(gateways))
		anyAvailable := false
		matchWithIPFS := false

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		ipfsContent, err := fetchContentFromIPFS(ctx, cidStr)
		if err != nil {
			log.Printf("Failed to fetch content from IPFS for CID %s: %s", cidStr, err)
			ipfsContent = nil
		} 
		cancel()

		ipfsHash := hashData(ipfsContent)

		for i, gateway := range gateways {
			gatewayCtx, gatewayCancel := context.WithTimeout(context.Background(), 1*time.Minute)
			// defer cancel()

			url := gateway + cidStr
			status, err := sendReqWithContext(gatewayCtx, url)
			gatewayCancel()
			if err != nil {
				log.Printf("[XX] Failed to fetch %s: %s", url, err)
				statuses[i] = "-"
				matches[i] = "-"		
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
				httpContent, err := fetchContentFromHTTP(context.Background(), url)
				if err != nil {
					log.Printf("Failed to fetch content from HTTP for URL %s: %s", url, err)
					matches[i] = "-"
					continue
				}

				// Comparing hashes
				if ipfsContent != nil {
					httpHash := hashData(httpContent)
					if ipfsHash == httpHash {
						matches[i] = "+"
						matchWithIPFS = true
					} else {
						matches[i] = "-"
					}
				} else {
					matches[i] = "-"
				}

			} else if status == http.StatusGone || status == http.StatusUnavailableForLegalReasons {
				log.Printf("[!!!] Content blocked at %s with status %d", url, status)
				statuses[i] = "x"
				matches[i] = "x"
			} else {
				log.Printf("[!!] Content not found at %s", url)
				statuses[i] = "-"
				matches[i] = "-"
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
	}
}

func checkIPFSLinks(filePathStr string, gateways []string, filePathActive string, filePathActiveTxt string, phishingCIDsFilePath string) {
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

	date := time.Now().Format("20060102")
	counter := 1
	var filePathMatchContent string
	for {
		filePathMatchContent = fmt.Sprintf("./collected_data/%s_matchcontent_%d.csv", date, counter)
		if _, err := os.Stat(filePathMatchContent); os.IsNotExist(err) {
			break
		}
		counter++
	}

	fileMatchContent, err := os.Create(filePathMatchContent)
	if err != nil {
		log.Fatalf("Failed to create match content CSV file: %s", err)
	}
	defer fileMatchContent.Close()
	matchContentWriter := csv.NewWriter(fileMatchContent)
	defer matchContentWriter.Flush()

	// Writes CSV headers
	header := append([]string{"CID", "accessibleOnIPFS"}, gateways...)
	if err := csvWriter.Write(header); err != nil {
		log.Fatalf("Failed to write CSV header: %s", err)
	}
	matchContentHeader := append([]string{"CID"}, gateways...)
	if err := matchContentWriter.Write(matchContentHeader); err != nil {
		log.Fatalf("Failed to write match content CSV header: %s", err)
	}

	ipfsRegex := regexp.MustCompile(`(ipfs://\S+|https?://[^\s]+/ipfs/[a-zA-Z0-9]+[^\s]*)`)
	scanner := bufio.NewScanner(file)

	var wg sync.WaitGroup
	linkChannel := make(chan string)

	processedCIDs := make(map[string]bool)
	var resultsLock sync.Mutex

	//Launching goroutines to send requests
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			processCIDs(linkChannel, gateways, csvWriter, matchContentWriter, fileActiveTxt, &resultsLock, processedCIDs)
		}()
	}

	//reading links and send to channel
	for scanner.Scan() {
		line := scanner.Text()
		matches := ipfsRegex.FindAllString(line, -1)
		for _, match := range matches {
			linkChannel <- match
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error scanning found-ipfs-phishing-links.txt: %s", err)
	}

	close(linkChannel)
	wg.Wait()

	// Read CIDs from the 'phishing_cids.csv' file and send them to the channel
	phishingCIDsFile, err := os.Open(phishingCIDsFilePath)
	if err != nil {
		log.Fatalf("Failed to open phishing CIDs file: %s", err)
	}
	defer phishingCIDsFile.Close()
	phishingCIDsScanner := bufio.NewScanner(phishingCIDsFile)

	//Launching another goroutines
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			processCIDs(linkChannel, gateways, csvWriter, matchContentWriter, fileActiveTxt, &resultsLock, processedCIDs)
		}()
	}

	
	for phishingCIDsScanner.Scan() {
		cid := phishingCIDsScanner.Text()
		linkChannel <- cid
	}
	if err := phishingCIDsScanner.Err(); err != nil {
		log.Printf("Error scanning phishing CIDs file: %s", err)
	}


	close(linkChannel)
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

// func sendReq(ctx context.Context, url string) (int, error) {
// 	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
// 	if err != nil {
// 		return 0, err
// 	}

// 	resp, err := httpClient.Do(req)
// 	if err != nil {
// 		return 0, err
// 	}
// 	defer resp.Body.Close()
// 	return resp.StatusCode, nil
// }

func sendReqWithContext(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	return resp.StatusCode, nil
}

func fetchContentFromHTTP(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	
	resp, err := httpClient.Do(req)
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

// func fetchContentFromIPFS(cidStr string, timeout time.Duration) ([]byte, error) {
// 	ctx, cancel := context.WithTimeout(context.Background(), timeout)
// 	defer cancel()

// 	sh := shell.NewShell("localhost:5001")
// 	rc, err := sh.Cat(cidStr)
// 	if err != nil {
// 		return nil, err
// 	}
// 	defer rc.Close()

// 	done := make(chan struct{})
// 	var data []byte
// 	go func() {
// 		data, err = io.ReadAll(rc)
// 		close(done)
// 	}()

// 	select {
// 	case <-done:
// 		return data, err
// 	case <-ctx.Done():
// 		return nil, ctx.Err()
// 	}
// }

func fetchContentFromIPFS(ctx context.Context, cidStr string) ([]byte, error) {
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

const denyListURL = "https://badbits.dwebops.pub/badbits.deny"

func downloadBadBitsList(url string, filePath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func fetchAndParseDenyList(filePath string) (map[string]bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	denyList := make(map[string]bool)
	scanner := bufio.NewScanner(file)
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

func convertCIDToBadBitsFormat(cidStr string) (string, error) {
	// Format the line into an ipfs:// URI
	// if strings.HasPrefix(cidStr, "/ipfs/") {
	// 	cidStr = strings.TrimPrefix(cidStr, "/ipfs/")
	// }
	cidStr = strings.TrimPrefix(cidStr, "/ipfs/")

	if !strings.HasPrefix(cidStr, "ipfs://") {
		cidStr = "ipfs://" + cidStr
	}

	u, err := url.Parse(cidStr)
	if err != nil {
		return "", fmt.Errorf("unable to build IPFS URI: %w", err)
	}

	// Extract necessary parts of the URI.
	c := u.Host
	path := u.EscapedPath()

	cc, err := cid.Parse(c)
	if err != nil {
		return "", fmt.Errorf("invalid CID: %w", err)
	}

	// Construct a v1 CID.
	v1Cid := cid.NewCidV1(cc.Type(), cc.Hash())

	// Append / or /<path>, if present.
	var out string
	if len(path) > 0 {
		out = fmt.Sprintf("%s/%s", v1Cid, path)
	} else {
		out = fmt.Sprintf("%s/", v1Cid)
	}

	// Hash and base16 encode the result.
	hasher := sha256.New()
	hasher.Write([]byte(out))
	encoded := hex.EncodeToString(hasher.Sum(nil))

	return encoded, nil
}

func findLatestCSV(directory string) (string, error) {
	var latestFile string
	var latestTime time.Time

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.Contains(info.Name(), "_found-ipfs-phishing-links_ACTIVE_") && strings.HasSuffix(info.Name(), ".csv") {
			if info.ModTime().After(latestTime) {
				latestTime = info.ModTime()
				latestFile = path
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	if latestFile == "" {
		return "", fmt.Errorf("no matching CSV files found in directory: %s", directory)
	}

	return latestFile, nil
}

func checkBadBits() {
	denyListFile := "src/badbits.deny"
	if err := downloadBadBitsList(denyListURL, denyListFile); err != nil {
		log.Fatalf("Failed to download bad bits denylist: %s", err)
	}
	log.Println("Bad bits denylist downloaded successfully.")

	denyList, err := fetchAndParseDenyList(denyListFile)
	if err != nil {
		log.Fatalf("Failed to parse bad bits denylist: %s", err)
	}

	collectedDataDir := "./collected_data"
	latestCSV, err := findLatestCSV(collectedDataDir)
	if err != nil {
		log.Fatalf("Failed to find the latest CSV: %s", err)
	}
	log.Printf("Latest CSV found: %s", latestCSV)

	file, err := os.Open(latestCSV)
	if err != nil {
		log.Fatalf("Failed to open the latest CSV: %s", err)
	}
	defer file.Close()

	date := time.Now().Format("20060102")
	var outputPath string
	counter := 1
	for {
		outputCSV := fmt.Sprintf("%s_badbits_check_%d.csv", date, counter)
		outputPath = filepath.Join(collectedDataDir, outputCSV)
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			break
		}
		counter++
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		log.Fatalf("Failed to create output CSV file: %s", err)
	}
	defer outFile.Close()
	csvWriter := csv.NewWriter(outFile)
	defer csvWriter.Flush()

	if err := csvWriter.Write([]string{"CID", "BadBits"}); err != nil {
		log.Fatalf("Failed to write CSV header: %s", err)
	}

	reader := csv.NewReader(file)
	_, err = reader.Read() // skip header
	if err != nil {
		log.Fatalf("Failed to read CSV header: %s", err)
	}

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("Failed to read CSV record: %s", err)
		}

		cidStr := record[0]
		hashedCID, err := convertCIDToBadBitsFormat(cidStr)
		if err != nil {
			log.Printf("Failed to convert CID to bad bits format: %s", err)
			continue
		}
		badBits := "-"
		if denyList[hashedCID] {
			badBits = "+"
		}
		if err := csvWriter.Write([]string{cidStr, badBits}); err != nil {
			log.Printf("Failed to write row to the CSV file: %s", err)
		}
	}

	log.Printf("Bad bits check completed. Results saved to: %s", outputPath)
}

func main() {
	initLogger()

	var extract bool
	flag.BoolVar(&extract, "e", false, "extract results live in CSV format")
	flag.Parse()

	if !checkIPFSDaemon() {
		log.Fatalf("IPFS daemon is not running. Please start the IPFS daemon and try again.")
	}

	fileURL := "https://raw.githubusercontent.com/mitchellkrogza/Phishing.Database/master/ALL-phishing-links.txt"
	filePath := "./phishing_db/ALL-phishing-links.txt"
	filePathIPFSLinks := "./phishing_db/found-ipfs-phishing-links.txt"
	phishingCIDsFile := "./src/phishing_cids.csv"

	//error handling for the collected_data directory
	if _, err := os.Stat("./collected_data"); os.IsNotExist(err) {
		if err := os.Mkdir("./collected_data", 0755); err != nil {
			log.Fatalf("Failed to create collected_data directory: %s", err)
		}
	}

	
	date := time.Now().Format("20060102")
	counter := 1
	// var filePathActiveIPFSLinks, 
	var filePathActiveIPFSLinks, filePathActiveTxt string
	for {
		filePathActiveIPFSLinks = fmt.Sprintf("./collected_data/%s_found-ipfs-phishing-links_ACTIVE_%d.csv", date, counter)
		filePathActiveTxt = fmt.Sprintf("./collected_data/%s_found-ipfs-phishing-links_ACTIVE_%d.txt", date, counter)
		if _, err := os.Stat(filePathActiveIPFSLinks); os.IsNotExist(err) {
			break
		}
		counter++	 
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

	checkIPFSLinks(filePathIPFSLinks, ipfsGateways, filePathActiveIPFSLinks, filePathActiveTxt, phishingCIDsFile)

	checkBadBits()
}
