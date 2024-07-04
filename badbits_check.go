package main

import (
	"bufio"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/minio/sha256-simd"
)

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