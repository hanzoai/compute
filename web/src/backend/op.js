// Copyright 2026 The Hanzo Authors. All Rights Reserved.
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

// How to read a TYPED op.
//
// A typed op answers the VALUE and states the outcome in the status: 200 with
// the thing, 201 when it made one, 204 when there is nothing to say, and a
// non-2xx carrying an RFC 9457 problem document whose `detail` is the message.
// There is no {status, msg, data} envelope, so there is nothing to unwrap and
// no logical failure hiding inside a 200.
//
// `answer` turns a response into what a caller wants — the value, or a
// rejection carrying the reason and the status:
//
//     AssetBackend.getAsset(owner, name)
//       .then(asset => ...)
//       .catch(err => Setting.showMessage("error", err.message));
//
// The routes still answering the envelope keep unwrapping it themselves; this
// is for the ones that have moved.
export async function answer(res) {
  if (res.status === 204) {
    return null;
  }
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    // `detail` is the problem document's message. `msg` is the filter chain's:
    // authorization still refuses in the old envelope, and it refuses BEFORE the
    // op runs, so both shapes reach a caller until the whole surface has moved.
    const err = new Error((body && (body.detail || body.msg)) || res.statusText || `HTTP ${res.status}`);
    err.status = res.status;
    throw err;
  }
  return body;
}
