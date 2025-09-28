package main

import (
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
	"log"
	"time"
)

// ----------------------------
// WebSocket Client
// ----------------------------
type TrafficLight struct {
	ID         string `msgpack:"id"`
	Program    string `msgpack:"program"`
	PhaseIndex int    `msgpack:"phaseIndex"`
	PhaseState string `msgpack:"phaseState"`
}

type TLSResponse struct {
	TrafficLights []TrafficLight `msgpack:"trafficLights"`
}

type StepResponse struct {
	SimTime float64 `msgpack:"simTime"`
}

func StepLoop(wsURL string, steps int) {
	var conn *websocket.Conn
	var err error

	// Endpoints
	tlReq := map[string]string{"endpoint": "trafficlights"}
	stepReq := map[string]string{"endpoint": "step"}
	stopReq := map[string]string{"endpoint": "stop"}

	stepMsg, _ := msgpack.Marshal(stepReq)
	tlMsg, _ := msgpack.Marshal(tlReq)
	stopMsg, _ := msgpack.Marshal(stopReq)

	// Connect to WebSocket
	for {
		conn, _, err = websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			log.Printf("failed to connect to WebSocket server: %v, retrying in 1s...", err)
			time.Sleep(1 * time.Second)
			continue
		}
		break
	}
	defer conn.Close()
	log.Println("connected to WebSocket server")

	for i := 0; i < steps; i++ {
		// --- TrafficLights request timing ---
		startTL := time.Now()
		if err := conn.WriteMessage(websocket.BinaryMessage, tlMsg); err != nil {
			log.Printf("failed to send trafficlights request: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		_, tlRespBytes, err := conn.ReadMessage()
		if err != nil {
			log.Printf("failed to read TLS response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}
		durationTL := time.Since(startTL)

		var tls TLSResponse
		if err := msgpack.Unmarshal(tlRespBytes, &tls); err != nil {
			log.Printf("failed to parse TLS response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// --- Step request timing ---
		startStep := time.Now()
		if err := conn.WriteMessage(websocket.BinaryMessage, stepMsg); err != nil {
			log.Printf("failed to send step request: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		_, stepRespBytes, err := conn.ReadMessage()
		if err != nil {
			log.Printf("failed to read step response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}
		durationStep := time.Since(startStep)

		var stepResp StepResponse
		if err := msgpack.Unmarshal(stepRespBytes, &stepResp); err != nil {
			log.Printf("failed to parse step response: %v", err)
			i--
			time.Sleep(500 * time.Millisecond)
			continue
		}

		fmt.Printf("Sim %.3f ms | Step %d %.3f ms | TrafficLights %.3f ms:\n",
			stepResp.SimTime*1000, i+1,
			float64(durationStep.Microseconds())/1000,
			float64(durationTL.Microseconds())/1000)

		for _, tl := range tls.TrafficLights {
			fmt.Printf("  ID: %s | Program: %s | Phase: %d | State: %s\n",
				tl.ID, tl.Program, tl.PhaseIndex, tl.PhaseState)
		}
	}

	if err := conn.WriteMessage(websocket.BinaryMessage, stopMsg); err != nil {
		log.Printf("failed to send stop request: %v", err)
		time.Sleep(500 * time.Millisecond)
	}
}

func main() {
	StepLoop("ws://127.0.0.1:5555", 10000)
}
