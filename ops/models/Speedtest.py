from typing import Any
from dataclasses import dataclass
import json


@dataclass
class Latency:
    iqm: float
    low: float
    high: float
    jitter: float

    @staticmethod
    def from_dict(obj: Any) -> 'Latency':
        _iqm = float(obj.get("iqm"))
        _low = float(obj.get("low"))
        _high = float(obj.get("high"))
        _jitter = float(obj.get("jitter"))
        return Latency(_iqm, _low, _high, _jitter)

@dataclass
class Upload:
    bandwidth: int
    bytes: int
    elapsed: int
    latency: Latency

    @staticmethod
    def from_dict(obj: Any) -> 'Upload':
        _bandwidth = int(obj.get("bandwidth"))
        _bytes = int(obj.get("bytes"))
        _elapsed = int(obj.get("elapsed"))
        _latency = Latency.from_dict(obj.get("latency"))
        return Upload(_bandwidth, _bytes, _elapsed, _latency)

@dataclass
class Download:
    bandwidth: int
    bytes: int
    elapsed: int
    latency: Latency

    @staticmethod
    def from_dict(obj: Any) -> 'Download':
        _bandwidth = int(obj.get("bandwidth"))
        _bytes = int(obj.get("bytes"))
        _elapsed = int(obj.get("elapsed"))
        _latency = Latency.from_dict(obj.get("latency"))
        return Download(_bandwidth, _bytes, _elapsed, _latency)

@dataclass
class Server:
    id: int
    host: str
    port: int
    name: str
    location: str
    country: str
    ip: str

    @staticmethod
    def from_dict(obj: Any) -> 'Server':
        _id = int(obj.get("id"))
        _host = str(obj.get("host"))
        _port = int(obj.get("port"))
        _name = str(obj.get("name"))
        _location = str(obj.get("location"))
        _country = str(obj.get("country"))
        _ip = str(obj.get("ip"))
        return Server(_id, _host, _port, _name, _location, _country, _ip)

@dataclass
class Interface:
    internalIp: str
    name: str
    macAddr: str
    isVpn: bool
    externalIp: str

    @staticmethod
    def from_dict(obj: Any) -> 'Interface':
        _internalIp = str(obj.get("internalIp"))
        _name = str(obj.get("name"))
        _macAddr = str(obj.get("macAddr"))
        _isVpn = bool(obj.get("isVpn"))
        _externalIp = str(obj.get("externalIp"))
        return Interface(_internalIp, _name, _macAddr, _isVpn, _externalIp)

@dataclass
class Ping:
    jitter: float
    latency: float
    low: float
    high: float

    @staticmethod
    def from_dict(obj: Any) -> 'Ping':
        _jitter = float(obj.get("jitter"))
        _latency = float(obj.get("latency"))
        _low = float(obj.get("low"))
        _high = float(obj.get("high"))
        return Ping(_jitter, _latency, _low, _high)

@dataclass
class Result:
    id: str
    url: str
    persisted: bool

    @staticmethod
    def from_dict(obj: Any) -> 'Result':
        _id = str(obj.get("id"))
        _url = str(obj.get("url"))
        _persisted = bool(obj.get("persisted"))
        return Result(_id, _url, _persisted)

@dataclass
class Speedtest:
    type: str
    timestamp: str
    ping: Ping
    download: Download
    upload: Upload
    packetLoss: int
    isp: str
    interface: Interface
    server: Server
    result: Result

    @staticmethod
    def from_dict(obj: Any) -> 'Speedtest':
        # default values (if not included)
        obj.setdefault("packetLoss", 0)
            
        _type = str(obj.get("type"))
        _timestamp = str(obj.get("timestamp"))
        _ping = Ping.from_dict(obj.get("ping"))
        _download = Download.from_dict(obj.get("download"))
        _upload = Upload.from_dict(obj.get("upload"))
        _packetLoss = int(obj.get("packetLoss"))
        _isp = str(obj.get("isp"))
        _interface = Interface.from_dict(obj.get("interface"))
        _server = Server.from_dict(obj.get("server"))
        _result = Result.from_dict(obj.get("result"))
        return Speedtest(_type,
                        _timestamp,
                        _ping,
                        _download,
                        _upload, 
                        _packetLoss,
                        _isp,
                        _interface,
                        _server,
                        _result)
    
    @staticmethod
    def from_json(json_string: str) -> 'Speedtest':
        return Speedtest.from_dict(json.loads(json_string))



# Example Usage
# jsonstring = json.loads(myjsonstring)
# root = Root.from_dict(jsonstring)
