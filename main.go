package main

import (
	log "github.com/sirupsen/logrus"
	"github.com/natefinch/lumberjack"
	"github.com/kelseyhightower/envconfig"
	"github.com/andrewtian/minepong"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"
	"io"
	"time"
	"fmt"
	"net"
	"encoding/base64"
	"crypto/hmac"
	"crypto/sha256"
	"strings"
	"net/http"
	"bytes"
	"io/ioutil"
)

// 1) (string) customerId
// 2) (string) resource
const DATA_COLLECTOR_ENDPOINT = "https://%s.ods.opinsights.azure.com%s?api-version=2016-04-01"
const HTTP_POST = "POST"
const JSON_CONTENT_TYPE = "application/json"
const RESOURCE = "/api/logs"

type Environment struct {
	// POD info
	PodName string `envconfig:"POD_NAME"`

	// Minecraft server info
	Host string `envconfig:"HOST"`
	Port string `envconfig:"PORT"`

	// LogAnalytics info
	CustomerId string `envconfig:"AZURE_CUSTOMER_ID"`
	SharedKey  string `envconfig:"AZURE_SHARED_KEY"`
}

type Stats struct {
	PodName       string
	OnlinePlayers int
	MaxPlayers    int
}

// https://docs.microsoft.com/en-us/azure/log-analytics/log-analytics-data-collector-api#python-sample
func buildSignature(customerId string, sharedKey string, date string, contentLength int, method string, resource string) string {
	headers := fmt.Sprintf("x-ms-date:%s", date)
	stringToHash := fmt.Sprintf("%s\n%d\n%s\n%s\n%s", method, contentLength, JSON_CONTENT_TYPE, headers, resource)
	bytesToHash, _ := base64.StdEncoding.DecodeString(sharedKey)
	mac := hmac.New(sha256.New, bytesToHash)
	mac.Write([]byte(stringToHash))
	expectedMAC := mac.Sum(nil)
	encodedHash := base64.StdEncoding.EncodeToString(expectedMAC)

	return fmt.Sprintf("SharedKey %s:%s", customerId, encodedHash)
}

func logSetup() {

	lumberjackFile := &lumberjack.Logger{
		Filename:   "./sidecar.log",
		MaxSize:    1,  // megabytes after which new file is created
		MaxBackups: 3,  // number of backups
		MaxAge:     28, //days
	}

	mw := io.MultiWriter(os.Stdout, lumberjackFile)
	log.SetOutput(mw)

}

func getServerStatus(env Environment) (status *minepong.Pong, err error) {
	hp := fmt.Sprintf("%s:%s", env.Host, env.Port)
	log.Info("Connecting to ", string(hp))
	connection, err := net.Dial("tcp", hp)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	log.Info("Connected to ", hp)

	state, err := minepong.Ping(connection, hp)
	if err != nil {
		return nil, err
	}

	return state, nil
}

func sendStats(env Environment, stats Stats) error {
	log.Info("Stats to send: ", stats)
	jsonBody, err := json.Marshal(&stats)
	if err != nil {
		return err
	}

	requestDate := time.Now().UTC().Format(time.RFC1123)
	requestDate = strings.Replace(requestDate, "UTC", "GMT", 1)
	signature := buildSignature(env.CustomerId, env.SharedKey, requestDate, len(jsonBody), HTTP_POST, RESOURCE)
	log.Infof("Signature: %s", signature)
	client := &http.Client{}
	request, err := buildHttpRequest("POST", env.CustomerId, "/api/logs", jsonBody, requestDate, signature)
	if (err != nil) {
		return err
	}
	log.Infof("Request:\n%s", formatRequest(request))
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := ioutil.ReadAll(response.Body)
	if (err != nil) {
		return err
	}
	log.Infof("Response (%s):\n%s", response.Status, string(body))
	return nil
}

// formatRequest generates ascii representation of a request
func formatRequest(r *http.Request) string {
	// Create return string
	var request []string
	// Add the request string
	url := fmt.Sprintf("%v %v %v", r.Method, r.URL, r.Proto)
	request = append(request, url)
	// Add the host
	request = append(request, fmt.Sprintf("Host: %v", r.Host))
	// Loop through headers
	for name, headers := range r.Header {
		name = strings.ToLower(name)
		for _, h := range headers {
			request = append(request, fmt.Sprintf("%v: %v", name, h))
		}
	}

	// If this is a POST, add post data
	if r.Method == "POST" {
		r.ParseForm()
		request = append(request, "\n")
		request = append(request, r.Form.Encode())
	}
	// Return the request as a string
	return strings.Join(request, "\n")
}

func buildHttpRequest(method string, customerId string, resource string, jsonBody []byte, requestDate string, signature string) (*http.Request, error) {
	req, err := http.NewRequest(method, fmt.Sprintf(DATA_COLLECTOR_ENDPOINT, customerId, resource), bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Add("Content-Type", JSON_CONTENT_TYPE)
	req.Header.Add("Authorization", signature)
	req.Header.Add("Log-Type", "MinecraftStats")
	req.Header.Add("x-ms-date", requestDate)
	return req, err
}

func extractStats(env Environment, serverStatus *minepong.Pong) Stats {
	return Stats{
		PodName:       env.PodName,
		OnlinePlayers: serverStatus.Players.Online,
		MaxPlayers:    serverStatus.Players.Max,
	}
}

func handleSigTermSignal() <-chan os.Signal {

	// Handle SIGTERM signal
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	return c
}

func getEnvironment(env *Environment) {
	err := envconfig.Process("sidecar", env)
	if err != nil {
		log.Fatal(err.Error())
	}
	log.Info("Config ", env)
}

func main() {

	var env Environment

	logSetup()

	getEnvironment(&env)

	log.Info("hello satanic kitty cats...")

	sigTermChannel := handleSigTermSignal()

	ticker := time.NewTicker(3 * time.Second)

	// run forever until signal
	for {
		select {
		case <-ticker.C:
			status, err := getServerStatus(env)
			if err != nil {
				log.Errorf("Error while checking server status: %s", err.Error())
				continue
			}
			jsonStatus, err := json.Marshal(status)
			if err != nil {
				log.Error(err)
				continue
			}
			log.Info("Minecraft server status ", string(jsonStatus))
			stats := extractStats(env, status)
			err = sendStats(env, stats)
			if (err != nil) {
				log.Error(err)
			}
		case <-sigTermChannel:
			log.Info("Fuck! You stopped me bastard!")
			ticker.Stop()
			os.Exit(0)
		default:
			log.Info("waiting...")
		}
		time.Sleep(1 * time.Second)
	}

}
