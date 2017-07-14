package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/signal"
	"time"

	"net/http"

	"github.com/alexellis/faas/gateway/queue"
	"github.com/alexellis/faas/gateway/requests"
	"github.com/nats-io/go-nats-streaming"
)

func printMsg(m *stan.Msg, i int) {
	log.Printf("[#%d] Received on [%s]: '%s'\n", i, m.Subject, m)
}

func makeClient() http.Client {
	proxyClient := http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   3 * time.Second,
				KeepAlive: 0,
			}).DialContext,
			MaxIdleConns:          1,
			DisableKeepAlives:     true,
			IdleConnTimeout:       120 * time.Millisecond,
			ExpectContinueTimeout: 1500 * time.Millisecond,
		},
	}
	return proxyClient
}

func main() {
	log.SetFlags(0)

	clusterID := "faas-cluster"
	val, _ := os.Hostname()
	clientID := "faas-worker-" + val

	var durable string
	var qgroup string
	var unsubscribe bool

	client := makeClient()
	sc, err := stan.Connect(clusterID, clientID, stan.NatsURL("nats://nats:4222"))
	if err != nil {
		log.Fatalf("Can't connect: %v\n", err)
	}

	startOpt := stan.StartWithLastReceived()
	i := 0
	mcb := func(msg *stan.Msg) {
		i++
		printMsg(msg, i)

		started := time.Now()

		req := queue.Request{}
		json.Unmarshal(msg.Data, &req)
		fmt.Printf("Request for %s.\n", req.Function)
		urlFunction := fmt.Sprintf("http://%s:8080/", req.Function)

		request, err := http.NewRequest("POST", urlFunction, bytes.NewReader(req.Body))
		res, err := client.Do(request)

		if err != nil {
			log.Println(err)
			timeTaken := time.Since(started).Seconds()

			postReport(&client, req.Function, http.StatusServiceUnavailable, timeTaken)
			return
		}

		if res.Body != nil {
			defer res.Body.Close()
			resData, err := ioutil.ReadAll(res.Body)
			if err != nil {
				log.Println(err)
			}
			fmt.Println(string(resData))
		}
		timeTaken := time.Since(started).Seconds()
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println(res.Status)

		postReport(&client, req.Function, res.StatusCode, timeTaken)

	}

	subj := "faas-request"
	qgroup = "faas"

	sub, err := sc.QueueSubscribe(subj, qgroup, mcb, startOpt, stan.DurableName(durable))
	if err != nil {
		log.Panicln(err)
	}

	log.Printf("Listening on [%s], clientID=[%s], qgroup=[%s] durable=[%s]\n", subj, clientID, qgroup, durable)

	// Wait for a SIGINT (perhaps triggered by user with CTRL-C)
	// Run cleanup when signal is received
	signalChan := make(chan os.Signal, 1)
	cleanupDone := make(chan bool)
	signal.Notify(signalChan, os.Interrupt)
	go func() {
		for _ = range signalChan {
			fmt.Printf("\nReceived an interrupt, unsubscribing and closing connection...\n\n")
			// Do not unsubscribe a durable on exit, except if asked to.
			if durable == "" || unsubscribe {
				sub.Unsubscribe()
			}
			sc.Close()
			cleanupDone <- true
		}
	}()
	<-cleanupDone
}

func postReport(client *http.Client, function string, statusCode int, timeTaken float64) {
	req := requests.AsyncReport{
		FunctionName: function,
		StatusCode:   statusCode,
		TimeTaken:    timeTaken,
	}

	reqBytes, _ := json.Marshal(req)
	request, err := http.NewRequest("POST", "http://gateway:8080/system/async-report", bytes.NewReader(reqBytes))
	res, err := client.Do(request)

	if err != nil {
		log.Println(err)
	}
	log.Printf("Posting report - %d\n", res.StatusCode)

}
