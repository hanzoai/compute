// Copyright 2023 The Hanzo Authors. All Rights Reserved.
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

import * as Setting from "../Setting";
import {answer} from "./op";

// An asset lives at /v1/assets, one at /v1/assets/{owner}/{name}, and the
// method says what to do with it. Every call here answers the value and rejects
// with the reason — see backend/op.js.

export function getAssets(owner, page = "", pageSize = "", field = "", value = "", sortField = "", sortOrder = "") {
  const q = new URLSearchParams({owner, p: page, pageSize, field, value, sortField, sortOrder});
  return fetch(`${Setting.ServerUrl}/v1/assets?${q}`, {
    method: "GET",
    credentials: "include",
  }).then(answer);
}

export function getAsset(owner, name) {
  return fetch(`${Setting.ServerUrl}/v1/assets/${encodeURIComponent(owner)}/${encodeURIComponent(name)}`, {
    method: "GET",
    credentials: "include",
  }).then(answer);
}

export function updateAsset(owner, name, asset) {
  return fetch(`${Setting.ServerUrl}/v1/assets/${encodeURIComponent(owner)}/${encodeURIComponent(name)}`, {
    method: "PUT",
    credentials: "include",
    body: JSON.stringify({asset: Setting.deepCopy(asset)}),
  }).then(answer);
}

export function addAsset(asset) {
  return fetch(`${Setting.ServerUrl}/v1/assets`, {
    method: "POST",
    credentials: "include",
    body: JSON.stringify({asset: Setting.deepCopy(asset)}),
  }).then(answer);
}

export function deleteAsset(asset) {
  return fetch(`${Setting.ServerUrl}/v1/assets/${encodeURIComponent(asset.owner)}/${encodeURIComponent(asset.name)}`, {
    method: "DELETE",
    credentials: "include",
  }).then(answer);
}

// openSession opens a remote session ON an asset — the session belongs to the
// asset, so it hangs off the asset's own address. It still answers the
// {status, msg, data} envelope, because the session collection it mints into
// has not moved yet.
export function openSession(owner, name, mode = "guacd") {
  return fetch(`${Setting.ServerUrl}/v1/assets/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/sessions?mode=${mode}`, {
    method: "POST",
    credentials: "include",
  }).then(res => res.json());
}
