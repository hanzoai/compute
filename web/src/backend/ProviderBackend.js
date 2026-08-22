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

// A cloud provider is ONE resource and the method carries the verb. Its identity
// is the pair (owner, name), so the pair is the address — the same owner/name the
// old ?id carried, in the part of the URL that addresses things.

function collection() {
  return `${Setting.ServerUrl}/v1/providers`;
}

function item(owner, name) {
  return `${collection()}/${encodeURIComponent(owner)}/${encodeURIComponent(name)}`;
}

// answer reads a response into what was asked for, or throws the reason with the
// status that carried it.
//
// These are typed ops: the answer IS the value and failure is the STATUS, in an
// RFC 9457 problem document. There is no envelope to unwrap and no "ok" that can
// be false while the request succeeded, so a caller either gets the thing or
// gets an error it can act on — 403 is a denial whatever language the message is
// in, which a prose match on the message could never be.
async function answer(res) {
  if (res.status === 204) {
    return null;
  }
  const body = await res.json().catch(() => null);
  if (res.ok) {
    return body;
  }
  const err = new Error((body && (body.detail || body.title || body.msg)) || res.statusText);
  err.status = res.status;
  throw err;
}

export function getProviders(owner, page = "", pageSize = "", field = "", value = "", sortField = "", sortOrder = "") {
  const query = new URLSearchParams({owner, p: page, pageSize, field, value, sortField, sortOrder});
  return fetch(`${collection()}?${query}`, {
    method: "GET",
    credentials: "include",
  }).then(answer);
}

export function getProvider(owner, name) {
  return fetch(item(owner, name), {
    method: "GET",
    credentials: "include",
  }).then(answer);
}

// The URL says WHICH provider is being replaced and the body says what it
// becomes, so the record is nested: the two names are two values, and a provider
// created under a generated name is renamed by sending a different one here.
export function updateProvider(owner, name, provider) {
  return fetch(item(owner, name), {
    method: "PUT",
    credentials: "include",
    body: JSON.stringify({provider}),
  }).then(answer);
}

export function addProvider(provider) {
  return fetch(collection(), {
    method: "POST",
    credentials: "include",
    body: JSON.stringify(provider),
  }).then(answer);
}

export function deleteProvider(provider) {
  return fetch(item(provider.owner, provider.name), {
    method: "DELETE",
    credentials: "include",
  }).then(answer);
}
