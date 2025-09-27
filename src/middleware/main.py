import os
import sys
import time
import traci
import asyncio
import msgpack
import subprocess
import websockets
from typing import Callable, Dict

server = None

# ----------------------
# Endpoint registry
# ----------------------
ENDPOINTS: Dict[str, Callable] = {}


def endpoint(func: Callable):
    ENDPOINTS[func.__name__] = func
    return func


# ----------------------
# SUMO setup
# ----------------------
def setup_sumo() -> list:
    os.environ["SUMO_HOME"] = "/usr/share/sumo"
    sys.path.append(os.path.join(os.environ["SUMO_HOME"], "tools"))
    os.environ["DISPLAY"] = ":1"
    sumoBinary = "sumo-gui"  # "sumo" for headless

    # Build network
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

    # Keep your exact command
    return [
        sumoBinary,
        "--window-size", "1920,1080",
        "--window-pos", "0,0",
        "-c", "sumo_config/config.sumocfg",
        "--start",
        "--quit-on-end",
    ]


# Cache
TL_IDS = []
TL_PROGRAMS = []
TL_CACHE = []


async def init_tl_cache():
    global TL_CACHE
    for tl in TL_IDS:
        TL_CACHE.append({
            "id": tl,
            "program": TL_PROGRAMS[tl],
            "phaseIndex": 0,
            "phaseState": ""
        })


# ----------------------
# SUMO control
# ----------------------
async def start_sumo():
    traci.start(setup_sumo())


# ----------------------
# Endpoints
# ----------------------
@endpoint
def step(params=None):
    start = time.time()
    traci.simulationStep()
    return {"simTime": time.time() - start}


@endpoint
def trafficlights(params=None):
    cache = TL_CACHE
    for tl_dict in cache:
        tl = tl_dict["id"]
        new_phase = traci.trafficlight.getPhase(tl)
        if new_phase != tl_dict["phaseIndex"]:
            tl_state = traci.trafficlight.getRedYellowGreenState(tl)
            tl_dict.update({
                "phaseIndex": new_phase,
                "phaseState": tl_state
            })
    return {"trafficLights": cache}


@endpoint
async def stop(params=None):
    traci.close()
    if server:
        await server.close()
    asyncio.get_event_loop().call_later(0.1, sys.exit, 0)
    return {"status": "stopping everything"}


# ----------------------
# WebSocket server
# ----------------------
async def ws_handler(websocket):
    async for message in websocket:
        try:
            req = msgpack.unpackb(message, raw=False)
            endpoint_name = req["endpoint"]
            params = req.get("params", {})
            func = ENDPOINTS.get(endpoint_name)

            if func:
                if asyncio.iscoroutinefunction(func):
                    result = await func(params)
                else:
                    result = func(params)
            else:
                result = {"error": "Unknown endpoint"}
        except Exception as e:
            result = {"error": str(e)}

        await websocket.send(msgpack.packb(result, use_bin_type=True))


# ----------------------
# Main
# ----------------------
async def main():
    global server, TL_IDS, TL_PROGRAMS, TL_CACHE
    await start_sumo()

    TL_IDS = traci.trafficlight.getIDList()
    TL_PROGRAMS = {tl: traci.trafficlight.getProgram(tl) for tl in TL_IDS}

    TL_CACHE = []
    for tl in TL_IDS:
        TL_CACHE.append({
            "id": tl,
            "program": TL_PROGRAMS[tl],
            "phaseIndex": traci.trafficlight.getPhase(tl),
            "phaseState": traci.trafficlight.getRedYellowGreenState(tl)
        })

    server = await websockets.serve(ws_handler, "0.0.0.0", 5555)
    await server.wait_closed()

if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("Server stopped manually")
