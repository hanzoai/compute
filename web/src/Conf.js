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

/**
 * Sign-in points at Hanzo IAM. It used to point at the upstream fork's
 * placeholder, and that made login on visor.hanzo.ai completely dead:
 *
 *   https://door.example.com/login/oauth/authorize?client_id=b108dacba027d…
 *     -> net::ERR_NAME_NOT_RESOLVED
 *
 * The host does not exist and never did — clicking sign in landed the user on
 * a browser error page. Nothing reported it, because the e2e check for this
 * surface asserted `url.includes('login') || /sign in|login|hanzo|visor/` and
 * then bailed out on `chrome-error`, so a dead sign-in read as green.
 *
 * `hanzo-visor` is a REAL application in IAM (`admin/hanzo-visor`, org `hanzo`,
 * "Hanzo Visor"), it grants `authorization_code`, and its allow-list already
 * contains `https://visor.hanzo.ai/callback` — which is what `redirectPath`
 * below composes. So the application was provisioned all along; only this file
 * was never pointed at it.
 *
 * `clientId` EQUALS `appName`: that holds for every one of IAM's applications,
 * and `app-visor` was doubly wrong — no IAM application has ever carried an
 * `app-` prefix, the convention is `<org>-<app>`.
 *
 * These are build-time constants, so a rebuild and deploy is what makes it real.
 *
 * WHITE-LABEL FOLLOW-UP: the same IAM application allow-lists
 * visor.lux.network, visor.zoo.ngo and visor.pars.network. Those brands resolve
 * their OWN issuer by hostname elsewhere in the fleet (lux.id / zoolabs.id /
 * pars.id) and would each need their own client; this static constant cannot
 * express that, and which clients exist at those issuers is unverified. Left
 * deliberately, rather than guessed at.
 */
export const AuthConfig = {
  serverUrl: "https://hanzo.id",
  clientId: "hanzo-visor",
  appName: "hanzo-visor",
  organizationName: "hanzo",
  redirectPath: "/callback",
};

export const ForceLanguage = "";
export const DefaultLanguage = "en";
export const IsDemoMode = false;

export const ThemeDefault = {
  themeType: "default",
  colorPrimary: "#000000",
  borderRadius: 6,
  isCompact: false,
};
