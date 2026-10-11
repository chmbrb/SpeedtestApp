#!/usr/bin/env python3
import sys
from pathlib import Path
from datetime import datetime
from ops.models import ProcessHandler



if __name__ == '__main__':
    command = f"{sys.argv[1]} --format=json"
    with Path("./log.txt").open("a") as p:
        p.write(f"{datetime.now()} - {command}\n")
    ph = ProcessHandler()
    ph.run_speedtest(command)