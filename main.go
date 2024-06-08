package main

import (
	"bufio"
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
)

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
		return fmt.Errorf("Bad Status: %s", resp.Status)
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

	if err:= scanner.Err(); err != nil {
		return err
	}

	return nil
}

func checkIPFSLinks(filePathStr string, gateways []string, filePathActive string, extract bool) {
	file, err := os.Open(filePathStr)
	if err != nil {
		log.Fatalf("Failed to open file: %s", err)
	}
	defer file.Close()

	// fileActive, err := os.Create(filePathActive)
	// if err != nil {
	// 	log.Fatalf("Failed to create file: %s", err)
	// }
	// defer fileActive.Close()

	var fileActive *os.File
	var csvWriter *csv.Writer
	if extract {
		_, err := os.Stat(filePathActive)
		isNewFile := os.IsNotExist(err)
		fileActive, err := os.OpenFile(filePathActive, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Printf("Failed to create or open file: %s", err)
		}
		defer fileActive.Close()
		csvWriter = csv.NewWriter(fileActive)
		defer csvWriter.Flush()

		if isNewFile{
			if err := csvWriter.Write([]string{"URL", "Status"}); err != nil {
				log.Printf("Failed to write CSV header: %s", err)
			}
		}
	} else {
		fileActive, err := os.Create(filePathActive)
		if err != nil {
			log.Printf("Failed to create file: %s", err)
			return
		}
		defer fileActive.Close()
	}


	ipfsRegex := regexp.MustCompile(`(ipfs://\S+|https?://[^\s]+/ipfs/[a-zA-Z0-9]+[^\s]*)`)
	scanner := bufio.NewScanner(file)


	var wg sync.WaitGroup
	linkChannel := make(chan string)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func ()  {
			defer wg.Done()
			for link := range linkChannel{
				cid := extractCID(link)
				for _, gateway := range gateways {
					url := gateway + cid
					status, err := sendReq(url)
					if err != nil {
						log.Printf("[XX] Failed to fetch %s: %s", url, err)
						continue
					}

					var statusText string 
					if status == http.StatusOK {
						log.Printf("[++] Content is available at %s", url)
						statusText = "available"
					} else if status == http.StatusForbidden {
						log.Printf("[--] Content is blocked at %s", url)
						// statusText = "blocked"
					} else {
						log.Printf("[!!] Content not found at %s", url)
						// statusText = "not found"
					}

					if extract {
						if err := csvWriter.Write([]string{url, cid}); err != nil {
							log.Printf("[XX] Failed to write link to the CSV file: %s", err)
						}
					} else {
						if statusText == "available" {
							if _, err := fileActive.WriteString(url + "available"); err != nil {
								log.Printf("Failed to write active link to the file: %s", err)
							}
						}
					}
				}
			}
		}()
	}

	for scanner.Scan() {
		line := scanner.Text()
		matches := ipfsRegex.FindAllString(line, -1)

		for _, match := range matches {
			linkChannel <- match
		}
		// for _, match := range matches {
		// 	cid := extractCID(match)
		// 	for _, gateway := range gateways {
		// 		url := gateway + cid
		// 		resp, err := sendReq(url)
		// 		if err != nil {
		// 			log.Printf("Failed to fetch %s: %s", url, err)
		// 			continue
		// 		}
				
		// 		if resp == http.StatusOK {
		// 			log.Printf("[--] Content is available at --> %s", url)
		// 			if _, err := fileActive.WriteString(url + "\n"); err != nil {
		// 				log.Printf("Failed to write active link to the file: %s", err)
		// 			}
		// 		} else {
		// 			log.Printf("[!] Content not found at - %s", url)
		// 		}
		// 		// resp.Body.Close()
		// 	}
		// }
	}
	close(linkChannel)

	wg.Wait()

	if err := scanner.Err(); err != nil {
		log.Printf("Error scanning file: %s", err)
	}
}

func extractCID(link string) string{
	if strings.HasPrefix(link, "ipfs://") {
		return strings.TrimPrefix(link, "ipfs://")
	}

	parts := strings.Split(link, "/ipfs/")
	if len(parts) < 2 {
		return ""
	}
	cidPart := parts[1]
	if pos:= strings.IndexAny(cidPart, " ?#"); pos >= 0 {
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

func main() {
	var extract bool
	flag.BoolVar(&extract, "e", false, "extract results live in CSV format")
	flag.Parse()

	fileURL := "https://raw.githubusercontent.com/mitchellkrogza/Phishing.Database/master/ALL-phishing-links.txt"
	filePath := "./phishing_db/ALL-phishing-links.txt"
	filePathIPFSLinks := "./phishing_db/found-ipfs-phishing-links.txt"
	filePathActiveIPFSLinks := "./phishing_db/found-ipfs-phishing-links_ACTIVE.txt"

	if extract {
		filePathActiveIPFSLinks = "./phishing_db/active.csv"
	}

	if err := downloadFile(fileURL, filePath); err != nil {
		log.Fatalf("[!!] error downloading db: %s", err)
	}

	log.Println("[++] DB downloaded successfully")

	
	if err := findIPFSLinks(filePath, filePathIPFSLinks); err != nil {
		log.Fatalf("Failed to find ipfs links: %s", err)
	}

	log.Println("[!!] ipfs links found and saved successfully.")

	// ipfsLinks, err := findIPFSLinks(filePath, filePathIPFSLinks)
	// if err != nil {
	// 	log.Fatalf("Failed to find ipfs links: %s", err)
	// }
	// log.Printf("Found ipfs links: %v", ipfsLinks)


	ipfsGateways := []string{
		"https://ipfs.io/ipfs/",
		"https://cloudflare-ipfs.com/ipfs/",
		"https://gateway.pinata.cloud/ipfs/",
		"https://dweb.link/ipfs/",
		"https://ipfs.eth.aragon.network/ipfs/",
		"https://trustless-gateway.link/ipfs/",
		"https://ipfs.runfission.com/ipfs/",
		"https://4everland.io/ipfs/",
		"https://w3s.link/ipfs/",
		"https://nftstorage.link/ipfs/",
		"https://hardbin.com/ipfs/",
		"https://storry.tv/ipfs/",
		"https://cf-ipfs.com/ipfs/",
	}
	checkIPFSLinks(filePathIPFSLinks, ipfsGateways, filePathActiveIPFSLinks, extract)
}