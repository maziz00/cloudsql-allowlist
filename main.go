package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

type authorizedNetworks struct {
	Value string `json:"value"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
}

type ipConfiguration struct {
	AuthorizedNetworks []authorizedNetworks `json:"authorizedNetworks"`
}

type settings struct {
	IpConfiguration ipConfiguration `json:"ipConfiguration"`
}

type cloudSql struct {
	Settings settings `json:"settings"`
}

func usage() {
	fmt.Println(`
	./app.go projectID cloudsqlID username ipaddress
	
	Example:
		projectID 		your google project ID				oneclick-dev-220409
		cloudsqlID		targeted cloudsql db instance			develop-20190219
		username		authorized network name				maziz-home
		ipaddress		IP address to be whitelisted			200.100.100.10/32

	Pre-requisite:
		this command require 4 arguments to execute
		and valid GDK priviliges
	`) 
}

func main(){

	// Part I
	// Request Json data from cloudsql instance
	
	args := os.Args[1:]

	if len(args) != 4 {
		usage()
		os.Exit(1)
	}

	projectId := os.Args[1]   		//targeted db project ID 			example: oneclick-dev-220409
	dataBaseId := os.Args[2] 		//targeted db name					example: develop-20190219-replica-1
	userName := os.Args[3]   		//username to be whitelisted		example: maziz
	authIP := os.Args[4]   			//ip to be whitelisted				example: 200.100.100.10

	getFromURL := "https://sqladmin.googleapis.com/sql/v1beta4/projects/" + projectId + "/instances/" + dataBaseId

	client1 := &http.Client{}

	req1, err := http.NewRequest("GET", getFromURL, nil)
	if err != nil {  
		log.Print(err)
		os.Exit(1)
	}

	cmd := exec.Command("gcloud", "auth", "print-access-token")
	stdout, err := cmd.Output()

    if err != nil {
        fmt.Println(err.Error())
        return
	}

	bearer := "Bearer " + strings.Trim(string(stdout), "\n")

	req1.Header.Set("Authorization", bearer)
	req1.Header.Set("Content-Type", "application/json; charset=utf-8")
	
	resp1, err := client1.Do(req1)

	if err != nil {
		panic(err)
	}

	defer resp1.Body.Close()

	body, err := ioutil.ReadAll(resp1.Body)

	if err != nil {
		panic(err)
	}

	// Part II
	// Perform json manipulation - addition in our case
	
	var nets cloudSql
	
	jsonFile := []byte(body)
	
	err = json.Unmarshal(jsonFile, &nets)
	
	if err != nil {
		fmt.Println("error:", err)
	}
	
	jsonAdd := authorizedNetworks{
		Value: authIP,
		Name: userName,
		Kind: "sql#aclEntry",
	}

	nets.Settings.IpConfiguration.AuthorizedNetworks = append(nets.Settings.IpConfiguration.AuthorizedNetworks, jsonAdd)
		
	fmt.Println(strings.Repeat("#", 50))
	byteFile, _ := json.MarshalIndent(nets, "", "  ")
	fmt.Println(string(byteFile))

	// Part III
	// Update cloudsql with new data - json format

	r := bytes.NewReader(byteFile)

	req2, _ := http.NewRequest("PATCH", getFromURL, r)

	client2 := &http.Client{}

	req2.Header.Set("Authorization", bearer)
	req2.Header.Set("Content-Type", "application/json; charset=utf-8")
	
	resp2, err := client2.Do(req2)

	if err != nil {
		panic(err)
	}

	defer resp2.Body.Close()

}