// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package util

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed 17monipdb.dat
var ipDbData embed.FS

func InitIpDb() {
	data, err := fs.ReadFile(ipDbData, "17monipdb.dat")
	if err != nil {
		panic(err)
	}

	InitWithData(data)
}

func GetDescFromIP(ip string) string {
	info, err := Find(ip)
	if err != nil {
		return ""
	}

	res := info.Country + ", " + info.Region + ", " + info.City
	if info.Isp != Null {
		res += ", " + info.Isp
	}

	return res
}

func GetIPInfo(clientIP string) string {
	if clientIP == "" {
		return ""
	}

	ips := strings.Split(clientIP, ",")
	res := ""
	for i := range ips {
		ip := strings.TrimSpace(ips[i])
		// desc := GetDescFromIP(ip)
		ipstr := fmt.Sprintf("%s: %s", ip, "")
		if i != len(ips)-1 {
			res += ipstr + " -> "
		} else {
			res += ipstr
		}
	}

	return res
}

func GetIPFromRequest(req *http.Request) string {
	return GetClientIP(req.Header.Get("x-forwarded-for"), req.RemoteAddr)
}

// GetClientIP formats the client IP description from the raw x-forwarded-for
// header and the transport remote address — the transport-agnostic core shared
// by the net/http path (GetIPFromRequest) and the ZAP path (a zip.Ctx hands the
// header plus its fasthttp RemoteAddr). x-forwarded-for wins; otherwise the
// remote address host is parsed (IPv4 host:port or bracketed IPv6).
func GetClientIP(xForwardedFor, remoteAddr string) string {
	clientIP := xForwardedFor
	if clientIP == "" {
		ipPort := strings.Split(remoteAddr, ":")
		if len(ipPort) >= 1 && len(ipPort) <= 2 {
			clientIP = ipPort[0]
		} else if len(ipPort) > 2 {
			idx := strings.LastIndex(remoteAddr, ":")
			clientIP = remoteAddr[0:idx]
			clientIP = strings.TrimLeft(clientIP, "[")
			clientIP = strings.TrimRight(clientIP, "]")
		}
	}

	return GetIPInfo(clientIP)
}
