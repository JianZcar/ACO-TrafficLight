package main

import (
	"encoding/binary"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// ----------------------------
// Config / ACO params
// ----------------------------
const (
	NUM_ANTS                = 8
	PHEROMONE_INIT          = 1.0
	EVAPORATION_RATE        = 0.4
	ALPHA                   = 1.0
	BETA                    = 4.0
	WAITING_TIME_THRESHOLD  = 10.0
	DEFAULT_COORD_DURATION  = 15
	RECONNECT_RETRY_SECONDS = 1
)

var PHASE_OPTIONS = []int{5, 10, 15, 20, 25, 30, 35}

// ----------------------------
// Msg types (same shapes used by Python server)
// ----------------------------
type TrafficLightPhase struct {
	State    string  `msgpack:"state"`
	Duration float64 `msgpack:"duration"`
}

type TrafficLightCycle struct {
	ProgramID string              `msgpack:"program"`
	Phases    []TrafficLightPhase `msgpack:"phases"`
}

type TrafficLight struct {
	ID             string            `msgpack:"id"`
	Program        string            `msgpack:"program"`
	PhaseIndex     int               `msgpack:"phaseIndex"`
	PhaseState     string            `msgpack:"phaseState"`
	PhaseRemaining float64           `msgpack:"phaseRemaining"`
	NextSwitch     float64           `msgpack:"nextSwitch"`
	Cycle          TrafficLightCycle `msgpack:"cycle"`
}

type TLSResponse struct {
	TrafficLights []TrafficLight `msgpack:"trafficLights"`
}

type JunctionLanesResponse struct {
	JunctionID    string   `msgpack:"junction_id"`
	IncomingEdges []string `msgpack:"incoming_edges"`
	IncomingLanes []struct {
		Lane    string   `msgpack:"lane"`
		LinksTo []string `msgpack:"links_to"`
	} `msgpack:"incoming_lanes"`
}

type EvaluateLaneResponse struct {
	Lane  string  `msgpack:"lane"`
	Queue int     `msgpack:"queue"`
	Wait  float64 `msgpack:"wait"`
}

type StepResponse struct {
	SimTime float64 `msgpack:"simTime"`
}

// ----------------------------
// Framing helpers (4-byte BE len + msgpack body)
// ----------------------------
func sendMsg(conn net.Conn, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := conn.Write(hdr[:]); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func readMsg(conn net.Conn) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(hdr[:])
	if length == 0 {
		return nil, nil
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ----------------------------
// UDS connect helper (blocking retries)
// ----------------------------
func connectUDS(socketPath string) net.Conn {
	for {
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			log.Printf("failed to connect to UDS %s: %v — retrying in %ds...", socketPath, err, RECONNECT_RETRY_SECONDS)
			time.Sleep(RECONNECT_RETRY_SECONDS * time.Second)
			continue
		}
		return conn
	}
}

// ----------------------------
// Helper: call endpoint
// ----------------------------
func callEndpoint(conn net.Conn, endpoint string, params any) ([]byte, error) {
	req := map[string]any{"endpoint": endpoint}
	if params != nil {
		req["params"] = params
	}
	payload, err := msgpack.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := sendMsg(conn, payload); err != nil {
		return nil, err
	}
	respBytes, err := readMsg(conn)
	if err != nil {
		return nil, err
	}
	return respBytes, nil
}

// ----------------------------
// ACO helpers
// ----------------------------
func sumQueuesAndWaits(conn net.Conn, tlID string, incomingLanes []string) (int, float64, error) {
	totalQueue := 0
	totalWait := 0.0
	for _, lane := range incomingLanes {
		respBytes, err := callEndpoint(conn, "evaluate_lane", map[string]any{"lane": lane})
		if err != nil {
			return 0, 0, err
		}
		var ev EvaluateLaneResponse
		if err := msgpack.Unmarshal(respBytes, &ev); err != nil {
			return 0, 0, err
		}
		totalQueue += ev.Queue
		totalWait += ev.Wait
	}
	return totalQueue, totalWait, nil
}

// chooseDuration uses pheromones + heuristic (mirrors python heuristic design)
func chooseDuration(tlID string, pheromones map[string]map[int]float64, nsInQueue int, ewInQueue int, queueLength int, waitingTime float64, downstreamQueue int) int {
	// Priority boost when one direction dominates
	maxIn := nsInQueue
	if ewInQueue > maxIn {
		maxIn = ewInQueue
	}
	queuePriority := 1.0
	if maxIn > 3 {
		queuePriority = 2.5
	}
	nsPriority := 1.0
	ewPriority := 1.0
	if nsInQueue == maxIn {
		nsPriority = queuePriority
	} else {
		ewPriority = queuePriority
	}

	heuristic := map[int]float64{}
	for _, dur := range PHASE_OPTIONS {
		queuePressure := float64(queueLength) + 1.0
		waitPressure := waitingTime + 1.0
		downPressure := float64(downstreamQueue) + 1.0
		efficiency := (queuePressure + waitPressure) / downPressure

		if nsInQueue > ewInQueue {
			if waitingTime > WAITING_TIME_THRESHOLD {
				heuristic[dur] = efficiency * (float64(dur) * nsPriority)
			} else {
				heuristic[dur] = efficiency * (1.0 / (float64(dur) + 1.0))
			}
		} else {
			if waitingTime > WAITING_TIME_THRESHOLD {
				heuristic[dur] = efficiency * (float64(dur) * ewPriority)
			} else {
				heuristic[dur] = efficiency * (1.0 / (float64(dur) + 1.0))
			}
		}
	}

	// combine pheromone & heuristic -> probabilities
	probs := map[int]float64{}
	total := 0.0
	for _, dur := range PHASE_OPTIONS {
		phi := pheromones[tlID][dur]
		if phi <= 0 {
			phi = PHEROMONE_INIT
		}
		val := math.Pow(phi, ALPHA) * math.Pow(heuristic[dur], BETA)
		probs[dur] = val
		total += val
	}
	if total == 0 {
		// fallback random
		return PHASE_OPTIONS[rand.Intn(len(PHASE_OPTIONS))]
	}
	// roulette wheel
	r := rand.Float64()
	acc := 0.0
	for _, dur := range PHASE_OPTIONS {
		acc += probs[dur] / total
		if r <= acc {
			return dur
		}
	}
	return PHASE_OPTIONS[len(PHASE_OPTIONS)-1]
}

func updatePheromones(pheromones map[string]map[int]float64, tlID string, duration int, avgQueue float64, avgWait float64) {
	queuePenalty := 1.0 + (avgQueue * 0.5)
	reward := 1.0 / (queuePenalty + avgWait + 1.0)
	cur := pheromones[tlID][duration]
	pheromones[tlID][duration] = (1.0-EVAPORATION_RATE)*cur + reward
}

// applyPhase sets the TL phase index and duration on the SUMO side by calling setphase endpoint
func applyPhase(conn net.Conn, tlID string, phaseIndex int, duration int) error {
	params := map[string]any{
		"tl":          tlID,
		"phase_index": phaseIndex,
		"duration":    float64(duration),
	}
	_, err := callEndpoint(conn, "setphase", params)
	return err
}

// coordinateTrafficLights: naive coordination - tells downstream TLs to set a clearing phase
func coordinateTrafficLights(conn net.Conn, tlID string, chosenDuration int, tlIDs []string) {
	// downstream mapping - same structure as python script
	downstreamMap := map[string][]string{
		"n1": {"n3", "n2"},
		"n2": {"n4", "n1"},
		"n3": {"n4", "n1"},
		"n4": {"n3", "n2"},
	}
	for _, down := range downstreamMap[tlID] {
		// only coordinate if we know this TL exists in current sim
		found := false
		for _, id := range tlIDs {
			if id == down {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		target := DEFAULT_COORD_DURATION
		if chosenDuration > target {
			target = chosenDuration
		}
		_ = applyPhase(conn, down, 0, target)
	}
}

// evaluateSolution: simulate applying duration to preferred phase and stepping SUMO N steps,
// then measure avg queue & wait at incoming lanes
func evaluateSolution(conn net.Conn, tlID string, incomingLanes []string, duration int, phaseIndex int) (float64, float64, error) {
	// attempt to set duration to the given phase index
	if err := applyPhase(conn, tlID, phaseIndex, duration); err != nil {
		// fallback: try other common index
		if phaseIndex == 0 {
			_ = applyPhase(conn, tlID, 2, duration)
		} else {
			_ = applyPhase(conn, tlID, 0, duration)
		}
	}

	// steps to simulate: clamp between 5 and min(15, duration)
	steps := int(math.Max(5, math.Min(15, float64(duration))))
	totalQ := 0
	totalW := 0.0
	for s := 0; s < steps; s++ {
		// step request
		_, err := callEndpoint(conn, "step", nil)
		if err != nil {
			return 0, 0, err
		}
		// measure lanes
		q, w, err := sumQueuesAndWaits(conn, tlID, incomingLanes)
		if err != nil {
			return 0, 0, err
		}
		totalQ += q
		totalW += w
	}
	avgQ := float64(totalQ) / float64(steps)
	avgW := totalW / float64(steps)
	return avgQ, avgW, nil
}

// ----------------------------
// Helpers for lane naming parsing
// ----------------------------

// laneEdge extracts edge id from a lane name like "E_in_0" -> "E_in"
// if the lane doesn't have underscores, returns the lane as-is.
func laneEdge(lane string) string {
	parts := strings.Split(lane, "_")
	if len(parts) <= 1 {
		return lane
	}
	// drop last token (lane index)
	return strings.Join(parts[:len(parts)-1], "_")
}

// edgeCompass looks at the first token of the edge id and returns a compass letter
// e.g. "E_in" -> "E", "edge_n1_n2" -> "edge" (fallback)
// We expect your naming to use single-letter compass tokens like E, W, S, N.
func edgeCompass(edge string) string {
	p := strings.Split(edge, "_")[0]
	return strings.ToUpper(p)
}

// classifyDirection returns "ns" or "ew" for the given lane using your naming:
// E / W -> east-west, N / S -> north-south. Unknown -> treat as east-west fallback.
func classifyDirectionFromLane(lane string) string {
	edge := laneEdge(lane)
	comp := edgeCompass(edge)
	switch comp {
	case "E", "W":
		return "ew"
	case "N", "S":
		return "ns"
	default:
		// fallback: treat as ew to avoid starving EW in odd maps
		return "ew"
	}
}

// ----------------------------
// Phase detection helpers & cache
// ----------------------------
type tlPhaseInfo struct {
	NSProgram string
	NSIndex   int
	EWIndex   int
}

// isGreenChar returns true for chars that we consider "green".
func isGreenChar(c byte) bool {
	return c == 'G' || c == 'g'
}

// findDirectionPhaseIndices inspects tl.Cycle.Phases and the incomingLanes order
// to choose the phase index that best serves NS and the one that best serves EW.
func findDirectionPhaseIndices(tl TrafficLight, incomingLanes []string) (nsIdx int, ewIdx int) {
	nsIdx = -1
	ewIdx = -1
	if len(tl.Cycle.Phases) == 0 {
		return 0, 0
	}

	// precompute direction for each lane index
	dirForIndex := make([]string, len(incomingLanes))
	for i, ln := range incomingLanes {
		dirForIndex[i] = classifyDirectionFromLane(ln)
	}

	bestNSCount := -1
	bestEWCount := -1

	for pi, ph := range tl.Cycle.Phases {
		state := ph.State
		L := len(incomingLanes)
		if len(state) < L {
			L = len(state)
		}
		nsCount := 0
		ewCount := 0
		for i := 0; i < L; i++ {
			if isGreenChar(state[i]) {
				if dirForIndex[i] == "ns" {
					nsCount++
				} else {
					ewCount++
				}
			}
		}
		if nsCount > bestNSCount {
			bestNSCount = nsCount
			nsIdx = pi
		}
		if ewCount > bestEWCount {
			bestEWCount = ewCount
			ewIdx = pi
		}
	}

	// fallback: try to find any phase that serves the direction
	if nsIdx == -1 || ewIdx == -1 {
		for pi, ph := range tl.Cycle.Phases {
			for i := 0; i < len(incomingLanes) && i < len(ph.State); i++ {
				if nsIdx == -1 && isGreenChar(ph.State[i]) && classifyDirectionFromLane(incomingLanes[i]) == "ns" {
					nsIdx = pi
				}
				if ewIdx == -1 && isGreenChar(ph.State[i]) && classifyDirectionFromLane(incomingLanes[i]) == "ew" {
					ewIdx = pi
				}
			}
		}
	}

	// conservative final fallback
	if nsIdx == -1 {
		nsIdx = 0
	}
	if ewIdx == -1 {
		ewIdx = 0
	}
	return nsIdx, ewIdx
}

// ----------------------------
// Main ACO loop
// ----------------------------
func RunACO(socketPath string, maxSteps int) {
	conn := connectUDS(socketPath)
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	log.Println("ACO controller connected to UDS server:", socketPath)

	// initialize pheromones map
	pheromones := map[string]map[int]float64{}

	previousQueues := map[string]map[string]int{} // tlID -> {"ns_in": x, "ew_in": y}
	currentGreenDirection := map[string]string{}

	// cache for phase indices per TL
	tlPhaseMap := map[string]tlPhaseInfo{}

	step := 0
	maxStepsLocal := maxSteps
	minSteps := 0

	rand.Seed(time.Now().UnixNano())

	for step < maxStepsLocal {
		tlRespBytes, err := callEndpoint(conn, "trafficlights", nil)
		if err != nil {
			log.Printf("trafficlights request failed: %v — reconnecting...", err)
			conn.Close()
			conn = connectUDS(socketPath)
			continue
		}

		var tls TLSResponse
		if err := msgpack.Unmarshal(tlRespBytes, &tls); err != nil {
			log.Printf("failed to parse trafficlights response: %v", err)
			_, _ = callEndpoint(conn, "step", nil)
			step++
			continue
		}

		// register pheromones for new TLs
		var tlIDs []string
		for _, t := range tls.TrafficLights {
			tlIDs = append(tlIDs, t.ID)
			if _, ok := pheromones[t.ID]; !ok {
				pheromones[t.ID] = map[int]float64{}
				for _, d := range PHASE_OPTIONS {
					pheromones[t.ID][d] = PHEROMONE_INIT
				}
			}
			if _, ok := previousQueues[t.ID]; !ok {
				previousQueues[t.ID] = map[string]int{"ns_in": 0, "ew_in": 0}
			}
			if _, ok := currentGreenDirection[t.ID]; !ok {
				currentGreenDirection[t.ID] = ""
			}
		}

		if step%3 == 0 {
			for _, tl := range tls.TrafficLights {
				// get junction lanes
				jReq := map[string]any{
					"junction_id": tl.ID,
				}
				jRespBytes, err := callEndpoint(conn, "junction_lanes", jReq)
				if err != nil {
					log.Printf("junction_lanes request failed for %s: %v", tl.ID, err)
					continue
				}
				var jl JunctionLanesResponse
				if err := msgpack.Unmarshal(jRespBytes, &jl); err != nil {
					log.Printf("failed to parse junction lanes for %s: %v", tl.ID, err)
					continue
				}
				// collect incoming lanes flat
				incomingLanes := []string{}
				for _, il := range jl.IncomingLanes {
					incomingLanes = append(incomingLanes, il.Lane)
				}

				// compute or refresh phase index mapping if needed
				info, ok := tlPhaseMap[tl.ID]
				if !ok || info.NSProgram != tl.Program {
					nsIdx, ewIdx := findDirectionPhaseIndices(tl, incomingLanes)
					tlPhaseMap[tl.ID] = tlPhaseInfo{
						NSProgram: tl.Program,
						NSIndex:   nsIdx,
						EWIndex:   ewIdx,
					}
					log.Printf("Detected phases for TL %s program=%s -> NS=%d EW=%d", tl.ID, tl.Program, nsIdx, ewIdx)
					info = tlPhaseMap[tl.ID]
				}

				// gather per-direction queues (uses your naming convention)
				nsIn := 0
				ewIn := 0
				for _, ln := range incomingLanes {
					respBytes, err := callEndpoint(conn, "evaluate_lane", map[string]any{"lane": ln})
					if err != nil {
						continue
					}
					var ev EvaluateLaneResponse
					if err := msgpack.Unmarshal(respBytes, &ev); err == nil {
						dir := classifyDirectionFromLane(ln)
						if dir == "ns" {
							nsIn += ev.Queue
						} else {
							ewIn += ev.Queue
						}
					}
				}

				// compute totals
				queueLength := 0
				waitingTime := 0.0
				for _, ln := range incomingLanes {
					respBytes, err := callEndpoint(conn, "evaluate_lane", map[string]any{"lane": ln})
					if err != nil {
						continue
					}
					var ev EvaluateLaneResponse
					if err := msgpack.Unmarshal(respBytes, &ev); err == nil {
						queueLength += ev.Queue
						waitingTime += ev.Wait
					}
				}

				// downstream queue (best-effort placeholder)
				downstreamQueue := 1

				// persistent queue detection (use detected indices)
				prev := previousQueues[tl.ID]
				if nsIn > 0 && prev["ns_in"] > 0 && nsIn >= prev["ns_in"] {
					nsPhase := info.NSIndex
					if nsPhase < 0 {
						nsPhase = 0
					}
					_ = applyPhase(conn, tl.ID, nsPhase, PHASE_OPTIONS[len(PHASE_OPTIONS)-1])
					log.Printf("Step %d: TL %s extended NS green due to persistent queue (%d) -> phase %d", step, tl.ID, nsIn, nsPhase)
					coordinateTrafficLights(conn, tl.ID, PHASE_OPTIONS[len(PHASE_OPTIONS)-1], tlIDs)
					previousQueues[tl.ID]["ns_in"] = nsIn
					continue
				} else if ewIn > 0 && prev["ew_in"] > 0 && ewIn >= prev["ew_in"] {
					ewPhase := info.EWIndex
					if ewPhase < 0 {
						ewPhase = 2
					}
					_ = applyPhase(conn, tl.ID, ewPhase, PHASE_OPTIONS[len(PHASE_OPTIONS)-1])
					log.Printf("Step %d: TL %s extended EW green due to persistent queue (%d) -> phase %d", step, tl.ID, ewIn, ewPhase)
					coordinateTrafficLights(conn, tl.ID, PHASE_OPTIONS[len(PHASE_OPTIONS)-1], tlIDs)
					previousQueues[tl.ID]["ew_in"] = ewIn
					continue
				}

				previousQueues[tl.ID]["ns_in"] = nsIn
				previousQueues[tl.ID]["ew_in"] = ewIn

				// zero-traffic shortcuts (use detected phases)
				if nsIn == 0 && ewIn > 0 {
					ewPhase := info.EWIndex
					if ewPhase < 0 {
						ewPhase = 2
					}
					_ = applyPhase(conn, tl.ID, ewPhase, PHASE_OPTIONS[len(PHASE_OPTIONS)-1])
					currentGreenDirection[tl.ID] = "east-west"
					coordinateTrafficLights(conn, tl.ID, PHASE_OPTIONS[len(PHASE_OPTIONS)-1], tlIDs)
					log.Printf("Step %d: TL %s continuous green to EW (NS empty) -> phase %d", step, tl.ID, ewPhase)
					continue
				} else if ewIn == 0 && nsIn > 0 {
					nsPhase := info.NSIndex
					if nsPhase < 0 {
						nsPhase = 0
					}
					_ = applyPhase(conn, tl.ID, nsPhase, PHASE_OPTIONS[len(PHASE_OPTIONS)-1])
					currentGreenDirection[tl.ID] = "north-south"
					coordinateTrafficLights(conn, tl.ID, PHASE_OPTIONS[len(PHASE_OPTIONS)-1], tlIDs)
					log.Printf("Step %d: TL %s continuous green to NS (EW empty) -> phase %d", step, tl.ID, nsPhase)
					continue
				}

				// large-queue immediate handling (use detected phases)
				if nsIn > ewIn && nsIn > 3 {
					nsPhase := info.NSIndex
					if nsPhase < 0 {
						nsPhase = 0
					}
					_ = applyPhase(conn, tl.ID, nsPhase, PHASE_OPTIONS[len(PHASE_OPTIONS)-1])
					coordinateTrafficLights(conn, tl.ID, PHASE_OPTIONS[len(PHASE_OPTIONS)-1], tlIDs)
					log.Printf("Step %d: TL %s extended NS green due to large queue (%d) -> phase %d", step, tl.ID, nsIn, nsPhase)
					continue
				} else if ewIn > nsIn && ewIn > 3 {
					ewPhase := info.EWIndex
					if ewPhase < 0 {
						ewPhase = 2
					}
					_ = applyPhase(conn, tl.ID, ewPhase, PHASE_OPTIONS[len(PHASE_OPTIONS)-1])
					coordinateTrafficLights(conn, tl.ID, PHASE_OPTIONS[len(PHASE_OPTIONS)-1], tlIDs)
					log.Printf("Step %d: TL %s extended EW green due to large queue (%d) -> phase %d", step, tl.ID, ewIn, ewPhase)
					continue
				}

				// ACO optimization
				bestDuration := 0
				bestScore := math.Inf(1)
				var chosenAvgQ, chosenAvgW float64

				// pick preferred phase based on queue comparison
				preferredPhase := info.EWIndex
				if nsIn > ewIn {
					preferredPhase = info.NSIndex
				}
				if preferredPhase < 0 {
					// fallback sensible default
					if nsIn > ewIn {
						preferredPhase = 0
					} else {
						preferredPhase = 2
					}
				}

				for ant := 0; ant < NUM_ANTS; ant++ {
					duration := chooseDuration(tl.ID, pheromones, nsIn, ewIn, queueLength, waitingTime, downstreamQueue)
					avgQ, avgW, err := evaluateSolution(conn, tl.ID, incomingLanes, duration, preferredPhase)
					if err != nil {
						log.Printf("evaluateSolution error for %s: %v", tl.ID, err)
						continue
					}
					score := avgQ + avgW
					updatePheromones(pheromones, tl.ID, duration, avgQ, avgW)
					if score < bestScore {
						bestScore = score
						bestDuration = duration
						chosenAvgQ = avgQ
						chosenAvgW = avgW
					}
				}

				if bestDuration > 0 {
					if nsIn > ewIn {
						nsPhase := info.NSIndex
						if nsPhase < 0 {
							nsPhase = 0
						}
						_ = applyPhase(conn, tl.ID, nsPhase, bestDuration)
						log.Printf("Step %d: TL %s set NS green (phase %d) for %ds (AvgQ=%.2f AvgW=%.2f)", step, tl.ID, nsPhase, bestDuration, chosenAvgQ, chosenAvgW)
					} else {
						ewPhase := info.EWIndex
						if ewPhase < 0 {
							ewPhase = 2
						}
						_ = applyPhase(conn, tl.ID, ewPhase, bestDuration)
						log.Printf("Step %d: TL %s set EW green (phase %d) for %ds (AvgQ=%.2f AvgW=%.2f)", step, tl.ID, ewPhase, bestDuration, chosenAvgQ, chosenAvgW)
					}
					coordinateTrafficLights(conn, tl.ID, bestDuration, tlIDs)
				}
			} // end for each TL
		} // end every 3 steps

		// advance simulation step (single step)
		_, err = callEndpoint(conn, "step", nil)
		if err != nil {
			log.Printf("step request failed: %v — reconnecting...", err)
			conn.Close()
			conn = connectUDS(socketPath)
			continue
		}

		step++
		if step%100 == 0 {
			log.Printf("Step %d completed", step)
		}
		if step >= maxStepsLocal && minSteps <= 0 {
			break
		}
	}

	// send stop
	_, _ = callEndpoint(conn, "stop", nil)
	log.Println("ACO controller finished and requested stop")
}

func main() {
	// Example invocation — adjust socket path and steps as needed:
	RunACO("/tmp/sumo_bridge.sock", 10000)
}

