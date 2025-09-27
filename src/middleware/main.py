import os
import sys
import subprocess
import threading
from typing import Optional

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

import traci

app = FastAPI(title="SUMO TraCI FastAPI bridge")

SIM_LOCK = threading.Lock()
TRACI_STARTED = False
SUMO_CMD = None


class StepRequest(BaseModel):
    steps: Optional[int] = 1


def setup_sumo() -> list:
    """
    Adjust this to match your existing setup. This function:
    - Makes sure SUMO env is set
    - Runs netconvert to build net.xml (as in your snippet)
    - Returns the command list to start sumo-gui or sumo (traci.start expects a list)
    """
    os.environ["SUMO_HOME"] = "/usr/share/sumo"
    sys.path.append(os.path.join(os.environ["SUMO_HOME"], "tools"))
    os.environ["DISPLAY"] = ":1"
    sumoBinary = "sumo-gui"  # or "sumo" for headless

    # build network (netconvert)
    subprocess.run(
        [
            "netconvert",
            "-n", "sumo_config/nodes.xml",
            "-e", "sumo_config/edges.xml",
            "-x", "sumo_config/connections.xml",
            "-o", "sumo_config/net.xml",
        ],
        check=True,
    )

    return [
        sumoBinary,
        "--window-size", "1920,1080",
        "--window-pos", "0,0",
        "-c",
        "sumo_config/config.sumocfg",
        "--start",
        "--quit-on-end",
    ]


def start_traci():
    """
    Starts traci once. Safe to call repeatedly; will start only if not started.
    """
    global TRACI_STARTED, SUMO_CMD
    if TRACI_STARTED:
        return

    SUMO_CMD = setup_sumo()
    try:
        traci.start(SUMO_CMD)
    except Exception as e:
        raise RuntimeError(f"Failed to start TraCI/SUMO: {e}")
    TRACI_STARTED = True


@app.on_event("startup")
def startup_event():
    """
    Called when FastAPI starts. We attempt to start SUMO/TraCI here.
    If you prefer lazy start (start only on first /step),
    remove this and call start_traci()
    inside endpoints instead. Current behavior: start at app startup.
    """
    try:
        start_traci()
    except Exception as e:
        print("Warning: failed to start SUMO on startup:", e)


@app.on_event("shutdown")
def shutdown_event():
    global TRACI_STARTED
    if TRACI_STARTED:
        try:
            traci.close()
        except Exception as e:
            print("Warning closing traci:", e)
        TRACI_STARTED = False


@app.post("/step")
def step_sim(req: StepRequest):
    """
    Step the SUMO simulation by req.steps steps (default 1).
    Will start TraCI if not started already (lazily).
    Returns the current simulation time and number of steps done.
    """
    if req.steps < 1:
        raise HTTPException(status_code=400, detail="steps must be >= 1")

    try:
        start_traci()
    except RuntimeError as e:
        raise HTTPException(status_code=500, detail=str(e))

    with SIM_LOCK:
        try:
            steps_done = 0
            for _ in range(req.steps):
                traci.simulationStep()
                steps_done += 1
            sim_time = traci.simulation.getTime()
        except Exception as e:
            raise HTTPException(
                status_code=500, detail=f"traci stepping failed: {e}")

    return {"steps": steps_done, "simTime": sim_time}


@app.get("/trafficlights")
def get_traffic_lights():
    """
    Return a compact snapshot for each traffic light:
    id, current program ID, current phase index, and the phase string (e.g. 'GrGr').
    """
    try:
        start_traci()
    except RuntimeError as e:
        raise HTTPException(status_code=500, detail=str(e))

    try:
        tl_ids = traci.trafficlight.getIDList()
        tl_data = []
        for tl in tl_ids:
            try:
                prog = traci.trafficlight.getProgram(tl)
            except Exception:
                prog = None
            try:
                phase_index = traci.trafficlight.getPhase(tl)
            except Exception:
                phase_index = None
            # get current phase state (string, e.g. "GrGr")
            try:
                phase_state = traci.trafficlight.getRedYellowGreenState(tl)
            except Exception:
                phase_state = None

            tl_data.append(
                {
                    "id": tl,
                    "program": prog,
                    "phaseIndex": phase_index,
                    "phaseState": phase_state,
                }
            )

    except Exception as e:
        raise HTTPException(
            status_code=500, detail=f"failed to query traffic lights: {e}")

    return {"trafficLights": tl_data}


@app.post("/stop")
def stop_sim():
    """
    Optional endpoint to gracefully stop/close traci and SUMO.
    After calling this, you'll need to restart the FastAPI service to re-create a SUMO instance,
    or call start_traci_if_needed() again (which runs setup_sumo -> netconvert -> traci.start).
    """
    global TRACI_STARTED
    if not TRACI_STARTED:
        return {"status": "already stopped"}

    try:
        traci.close()
        TRACI_STARTED = False
    except Exception as e:
        raise HTTPException(
            status_code=500, detail=f"failed to close traci: {e}")

    return {"status": "stopped"}
