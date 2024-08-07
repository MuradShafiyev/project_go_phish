package main

import (
	// "context"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	// "os/exec"
	"path/filepath"
	"strings"
	"time"

	shell "github.com/ipfs/go-ipfs-api"
)

// EnsureIPFSDaemon ensures the IPFS daemon is running
// func EnsureIPFSDaemon() error {
// 	for {
// 		// Check if the daemon is already running
// 		checkCmd := exec.Command("pgrep", "-f", "ipfs daemon")
// 		if err := checkCmd.Run(); err == nil {
// 			log.Println("IPFS daemon is already running.")
// 			return nil
// 		}

// 		// If not running, attempt to start the daemon
// 		log.Println("Starting IPFS daemon...")
// 		startCmd := exec.Command("ipfs", "daemon")
// 		if err := startCmd.Start(); err != nil {
// 			log.Printf("Failed to start IPFS daemon: %v", err)
// 		} else {
// 			log.Println("IPFS daemon started successfully.")
// 		}

// 		time.Sleep(10 * time.Second) // Wait before the next check to ensure it starts
// 	}
// }

// Function to fetch content from IPFS with a timeout
// func fetchContentFromIPFSWithTimeout(cidStr string, timeout time.Duration) ([]byte, error) {
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

// Function to download content recursively from IPFS and save to a specified folder
func downloadContentRecursive(cid, outputDir string, sh *shell.Shell, timeout time.Duration) error {
	// Check if CID is a directory
	links, err := sh.List(cid)
	if err != nil {
		return fmt.Errorf("failed to list CID %s: %v", cid, err)
	}

	if len(links) > 0 {
		// CID is a directory
		newOutputDir := filepath.Join(outputDir, cid)
		if err := os.MkdirAll(newOutputDir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %v", newOutputDir, err)
		}

		for _, link := range links {
			if err := downloadContentRecursive(link.Hash, newOutputDir, sh, timeout); err != nil {
				log.Printf("Failed to download content for CID %s: %v\n", link.Hash, err)
			}
		}
	} else {
		// CID is a file
		rc, err := sh.Cat(cid)
		if err != nil {
			return fmt.Errorf("failed to fetch content for CID %s: %v", cid, err)
		}
		defer rc.Close()

		content, err := io.ReadAll(rc)
		if err != nil {
			return fmt.Errorf("failed to read content for CID %s: %v", cid, err)
		}

		outputPath := filepath.Join(outputDir, filepath.Base(cid))
		if err := os.WriteFile(outputPath, content, 0644); err != nil {
			return fmt.Errorf("failed to write content to file for CID %s: %v", cid, err)
		}
		log.Printf("Content for CID %s saved to %s\n", cid, outputPath)
	}

	return nil
}

// Function to download content for a list of CIDs and save to a specified folder
func downloadContent(cids []string, outputDir string, timeout time.Duration) error {
	// Create the output directory if it doesn't exist
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %v", err)
		}
	}

	sh := shell.NewShell("localhost:5001")

	for _, cid := range cids {
		log.Printf("Downloading content for CID: %s\n", cid)
		for retries := 0; retries < 3; retries++ {
			err := downloadContentRecursive(cid, outputDir, sh, timeout)
			if err != nil {
				if strings.Contains(err.Error(), "connection refused") {
					log.Printf("Failed to connect to IPFS daemon. Retrying... (%d/3)\n", retries+1)
					time.Sleep(5 * time.Second) // Wait before retrying
					continue
				}
				log.Printf("Failed to download content for CID %s: %v\n", cid, err)
			} else {
				break
			}
		}
	}

	return nil
}

// Function to read CIDs from a CSV file
func readCIDsFromCSV(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Read() // skip the header row

	var cids []string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read CSV file: %v", err)
		}
		cid := record[0]
		if cid != "" {
			cids = append(cids, cid)
		}
	}

	return cids, nil
}

func download_content() {
	// Ensure the IPFS daemon is running before proceeding
	// if err := EnsureIPFSDaemon(); err != nil {
	// 	log.Fatalf("Failed to ensure IPFS daemon is running: %v", err)
	// }

	// Define the path to the CSV file containing CIDs
	csvFilePath := "./collected_data/20240716_found-ipfs-phishing-links_ACTIVE_2.csv"
	outputDir := "./downloaded_content"
	timeout := 1 * time.Minute

	cids, err := readCIDsFromCSV(csvFilePath)
	if err != nil {
		log.Fatalf("Failed to read CIDs from CSV file: %v\n", err)
	}

	if err := downloadContent(cids, outputDir, timeout); err != nil {
		log.Fatalf("Failed to download content: %v\n", err)
	}
}