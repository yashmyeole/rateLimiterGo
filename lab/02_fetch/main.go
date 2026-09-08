package main

import (
	"fmt"
	"net/http" // the same package our proxy and backends will be built on
	"os"       // operating-system things: command-line args, exit codes
	"time"     // durations like 5 * time.Second
)

func main() {
	
	url := "https://go.dev"

	
	if len(os.Args) > 1 {
		url = os.Args[1]
	}

	client := &http.Client{Timeout: 5 * time.Second}

	
	resp, err := client.Get(url)
	if err != nil {
		fmt.Println("request failed:", err)
		os.Exit(1) 
	}

	defer resp.Body.Close()

	fmt.Println(url, "->", resp.Status)
	fmt.Println("server header:", resp.Header.Get("Server"))
}
