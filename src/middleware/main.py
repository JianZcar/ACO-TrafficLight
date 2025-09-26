import subprocess
import os
import sys
import traci


def setup_sumo() -> list:
    os.environ["SUMO_HOME"] = "/usr/share/sumo"
    sys.path.append(os.path.join(os.environ["SUMO_HOME"], "tools"))
    os.environ["DISPLAY"] = ":1"
    sumoBinary = "sumo-gui"

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
        "-c", "sumo_config/config.sumocfg",
        "--start",
        "--quit-on-end"
    ]


sumo_cmd = setup_sumo()

traci.start(sumo_cmd)
tl_ids = traci.trafficlight.getIDList()

step = 0
while step <= 1000:
    traci.simulationStep()
    step += 1

traci.close()
