// Copyright 2024 The Hanzo Authors. All Rights Reserved.
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
import {Connected} from "../SessionListPage";

// A session is addressed by the pair that identifies it, both path segments, and
// the method says what to do with it. The SPA carries that pair composed, as
// `owner/name`, which is how the store addresses a row too — so this is the one
// place it is taken apart.
function at(sessionId) {
  const [owner, ...rest] = String(sessionId).split("/");
  return `${Setting.ServerUrl}/v1/sessions/${encodeURIComponent(owner)}/${encodeURIComponent(rest.join("/"))}`;
}

// The session ops are typed, so there is no {status, msg, data} envelope: the
// answer IS the value and the status IS the outcome. read resolves the value
// (nothing, for a 204) and rejects with what the server said, carrying the
// status so a caller can tell a refusal from a failure.
function read(res) {
  if (res.status === 204) {
    return Promise.resolve();
  }
  return res.json().then(body => {
    if (res.ok) {
      return body;
    }
    const error = new Error(body?.detail || body?.title || res.statusText);
    error.status = res.status;
    return Promise.reject(error);
  });
}

export function getSessions(owner, page = "", pageSize = "", field = "", value = "", sortField = "", sortOrder = "", status = Connected) {
  return fetch(`${Setting.ServerUrl}/v1/sessions?owner=${owner}&page=${page}&pageSize=${pageSize}&field=${field}&value=${value}&sortField=${sortField}&sortOrder=${sortOrder}&status=${status}`, {
    method: "GET",
    credentials: "include",
  }).then(read);
}

export function getSession(owner, name) {
  return fetch(at(`${owner}/${name}`), {
    method: "GET",
    credentials: "include",
  }).then(read);
}

export function updateSession(owner, name, session) {
  return fetch(at(`${owner}/${name}`), {
    method: "PUT",
    credentials: "include",
    body: JSON.stringify(Setting.deepCopy(session)),
  }).then(read);
}

export function addAssetTunnel(assetId, mode = "guacd") {
  return fetch(`${Setting.ServerUrl}/v1/add-asset-tunnel?assetId=${assetId}&mode=${mode}`, {
    method: "POST",
    credentials: "include",
  }).then(res => res.json());
}

export function deleteSession(session) {
  return fetch(at(Setting.GetIdFromObject(session)), {
    method: "DELETE",
    credentials: "include",
  }).then(read);
}

// The live connection is a sub-resource of the session, not a value of its
// status column: closing it leaves the record, closed, while deleting the
// session removes the record itself.
export function connect(sessionId) {
  return fetch(`${at(sessionId)}/connection`, {
    method: "PUT",
    credentials: "include",
  }).then(read);
}

export function disconnect(sessionId) {
  return fetch(`${at(sessionId)}/connection`, {
    method: "DELETE",
    credentials: "include",
  }).then(read);
}
