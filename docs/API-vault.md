# Credentials (vault)

Reference: [Bitwarden](https://bitwarden.com). A password manager on the board: the `credentials` card shows the hub's one vault, and the key button in the header opens the same vault in a window from any page, without a card. Package: `internal/vault`, handler `internal/server/h_vault.go`, client `web/src/vault/` (header window: `Window.tsx`, shared content: `Panel.tsx`) and `web/src/board/tiles/CredentialsTile.tsx`.

## Why encrypt in the browser

Agents on This PC run as the same user as the hub, so they can read `<data>` (PROJECT §6). If the key were in `secret.key`, like the SSH credentials, every agent could read every password. That is why the vault follows Bitwarden's approach: **the master password never leaves the browser**, and the hub stores only ciphertext.

```
master password --Argon2id--> wrapping key --AES-256-GCM--> vault key --AES-256-GCM--> item
```

| Part | Value |
|---|---|
| KDF | Argon2id, 64 MiB, 3 passes, parallelism 4 (Bitwarden's default), 16-byte salt, 32-byte output. The password is NFKC-normalized first. Runs in a worker (`hash-wasm`) |
| Vault key | 32 random bytes, never changes. Once unlocked, only in memory: a non-extractable `CryptoKey` on HTTPS and localhost, plain bytes on plain HTTP (see below) |
| Key wrapping | AES-256-GCM, AAD `nextboard-vault/v1/key`. Shape: nonce 12 + ciphertext 32 + tag 16, base64 |
| Item | AES-256-GCM, AAD `nextboard-vault/v1/item/<id>`, so a blob cannot be moved to another id. The JSON is padded with spaces to a multiple of 512 bytes, so the blob's size does not leak the length of its contents |

A wrong password shows as a failed GCM tag: the hub stores no hash of any kind and never knows that an unlock was attempted.

The format can be opened without NextBoard code: Node's `crypto.argon2Sync` + AES-GCM is enough, and that is what the end-to-end test checks.

## Plain HTTP

This hub is local-first and is often opened over `http://` on one's own network, so the vault works there too (owner decision). Browsers hide `crypto.subtle` and `navigator.clipboard` outside HTTPS and localhost, so:

| Part | HTTPS, localhost | Plain HTTP |
|---|---|---|
| Argon2id | `hash-wasm` in a worker | Same |
| AES-256-GCM | WebCrypto, non-extractable key | `@noble/ciphers`, the key as bytes in memory, overwritten with zeros when locked. Loaded only here |
| Random | `crypto.getRandomValues` | Same |
| TOTP | `@noble/hashes` | Same |
| Copy | `navigator.clipboard` | `execCommand`, straight from the click |
| Clearing the clipboard | On time | Browsers allow it only about 5 seconds after input, so past that it happens on the next click or key press on this page. Filled with one space. The toast and the settings dialog say so |

The resulting bytes are exactly the same, so one vault can be opened from both sides; the end-to-end test writes on one side and opens on the other, in both directions. The create-vault and unlock screens state the risk below the password field.

## Blob contents

Types follow Bitwarden: `login`, `card`, `identity`, `note`, `ssh`. The fields per type are in `TYPE_SCHEMA` (`web/src/vault/model.ts`).

```jsonc
{ "k": "item", "type": "login", "name": "GitHub", "notes": "", "favorite": false, "folderId": "",
  "values": { "username": "", "password": "", "totp": "" }, "uris": [""],
  "fields": [{ "name": "", "value": "", "hidden": true }],
  "history": [{ "password": "", "at": "…" }],   // ≤ 5, the login's previous passwords
  "createdAt": "…", "updatedAt": "…", "deletedAt": "…" }   // deletedAt = in the trash
```

Folders are encrypted records too: `{ "k": "folder", "name": "…" }`. The hub cannot tell items from folders, and does not know names, types, folders or trash status.

The trash lives inside the blob, so the browser does the deleting: items more than 30 days in the trash are deleted at the next unlock.

## Passkeys of other sites

A site in a browser tile can keep its passkeys in the vault. The page cannot be reached from outside without an extension, so the hub provides an extension that replaces `navigator.credentials` for passkey requests. Off until turned on in the vault menu, **Passkeys for sites…**.

```
site in tile → extension (background.js: origin from the browser) → hub (loopback, token) → dialog in the owner's browser
                                                                                            ↓ the key signs here
site ← result ← extension ← hub ←───────────────────────────────────────────────────────── result without the key
```

- **Zero-knowledge still holds:** the P-256 private key is in the login item's blob (`passkeys[]`) and signs in the owner's browser with `@noble/curves`, because `crypto.subtle` does not exist on plain HTTP. The hub and the extension see only the site, the account name, the challenge and the signature result, and only in memory while the page waits.
- **Every use is asked.** The dialog opens by itself because the site is waiting, for at most 2 minutes. **Save in vault** or **Sign in as**, **Use the browser's own**, or **Refuse**. A locked vault asks to be unlocked in that dialog.
- **The site's origin comes from the browser**, `sender.origin` in the service worker, not from the page. The hub checks the WebAuthn rules: a secure page (HTTPS or localhost), the rpId is that host or a domain above it, and it is not a public suffix (`golang.org/x/net/publicsuffix`). Frames from another site are handed to the browser. Whatever fails the check is also handed to the browser, which refuses it with the error the page expects.
- **What is handed to the browser:** sites that do not accept ES256, autofill (`mediation: "conditional"`), a request while the same site is already waiting, more than 3 requests at once, and no answer. A page cannot flood the owner with dialogs.
- **Shape:** attestation `none`, zero AAGUID, counter 0, flags UP, UV, BE, BS. UV is claimed because the vault is unlocked and a person approves. Backup-eligible because the blob is part of the export.
- **Export:** `nextboardPasskeys` next to the Bitwarden format. Bitwarden does not read it, so passkeys move only between NextBoards.

**Installing the extension.** Its files are embedded in the binary (`internal/gui/passkeyext`) and written to `<data>/gui/passkey-extension/` together with a `config.json` holding the loopback address and the token. The hook port changes at every hub start, so `config.json` is rewritten at each start and the extension reads it again on every request.

| Browser | How | Status |
|---|---|---|
| Chromium, Brave, Edge, Vivaldi, Opera | `--load-extension` automatically when the browser starts | Tested on Chromium |
| Google Chrome 137 and later | **Once, by hand**: `chrome://extensions`, Developer mode, Load unpacked. The dialog guides you. The hub cannot open that page: from the command line, Chrome 156 only opens New Tab | The manual path has not been tried end to end |
| Firefox | Not supported, needs a signed XPI | — |

A browser from snap or flatpak reads a copy in its own folder, written when the browser starts, so after a hub restart that browser needs to be started again to get the new address. This path has not been tried with a real browser.

Why Chrome has to be manual, tried on Chrome 156: `--load-extension` is ignored, the `External Extensions` folder in the profile is ignored, and `Extensions.loadUnpacked` through the debugging pipe does not survive closing the browser. A pipe kept open also makes `navigator.webdriver` `true`, a sign that makes sites refuse logins, while that is the very reason a real browser is used.

| Route | Listener | Contents |
|---|---|---|
| POST `/nb/passkey` | loopback, `Authorization: Bearer` | `{"kind":"create"\|"get","origin","options"}` → `{"result"}`, `{"fallback":true}`, or `{"error":{"name","message"}}`. Waits at most 2 minutes |
| GET `/nb/passkey/hello` | loopback, token | The extension reports that it is running |
| GET `/api/vault/passkeys` | login | `{"enabled","active","path"?,"requests":[{id,kind,origin,rpId,options,createdAt}]}` |
| PUT `/api/vault/passkeys` | login | `{"enabled"}`. Turning it off drops the token, so the extension's copy in the browser stops working |
| POST `/api/vault/passkeys/requests/{id}` | login | `{"result"}`, `{"fallback":true}`, or `{"refuse":true}`. `404 request_gone` |

The `passkeyRequest` event tells the web to reload.

**Trust boundary.** The token is in the hub's data folder, and a program on This PC running as the same user can read it and then send requests in the name of any site. The owner still has to approve them, but in that case the site name in the dialog cannot be proven, so the dialog asks to approve only what was just done. This is the same limit as with agents, which can replace the hub binary or the web files. Evidence in tests: the WebAuthn ceremony is verified with `node:crypto` the way a relying party does, and end to end with a real Chromium.

## API

All through the ordinary login, with an `X-NextBoard: 1` header on those that change data. Those that could lock the owner out are admin only, not for device tokens.

| Route | Access | Contents |
|---|---|---|
| `GET /api/vault` | login | `{"exists","header","items":[{"id","blob","rev","updatedAt"}],"rev"}` |
| `POST /api/vault` | admin | Create the vault. Body `{"kdf":{"name":"argon2id","m","t","p"},"salt","wrappedKey"}` → 201 header. 409 `vault_exists` |
| `PUT /api/vault/key` | admin | Change the master password: same body + `baseKeyRev`. Items are not rewritten. 409 `conflict` |
| `PUT /api/vault/items/{id}` | login | `{"blob","baseRev"}`, `baseRev` 0 for a new item. 409 `conflict` when it has changed |
| `DELETE /api/vault/items/{id}?baseRev=` | login | Delete permanently. 404 `item_not_found`, 409 `conflict` |
| `POST /api/vault/batch` | login | `{"items":[{"id","blob"}]}`, for import, all or nothing |
| `POST /api/vault/reset` | admin | `{"confirm":"RESET"}` → `{"ok","kept"}` |
| `POST /api/vault/pins` | login | `{"proof"}` → 201 `{"id","secret"}` (see PIN) |
| `POST /api/vault/pins/{id}/open` | login | `{"proof"}` → `{"secret"}`. Wrong: 403 `wrong_pin` + `left`. Used up: 410 `pin_gone` |
| `DELETE /api/vault/pins/{id}` | login | Turn off one browser's PIN |
| `DELETE /api/vault/pins` | admin | Turn off all PINs, for a lost browser |
| `GET /api/vault/pwned/{prefix}` | login | A 5-character uppercase hexadecimal prefix → the HIBP range API's response text as is (suffix:count, padded). 400 when it is not a prefix; 502 `pwned_unreachable` / `pwned_failed` |

The hub validates only the shape: id `^[A-Za-z0-9_-]{8,64}$`, blob base64 ≥ 28 bytes, KDF parameters within limits (m 32 MiB–1 GiB, t 2–16, p 1–16). A blob that is not valid base64 of at least 28 bytes is rejected, but the hub cannot make sure its contents are really encrypted: that is the client's job.

Event on `/api/events`: `{"type":"vaultChanged","rev","vaultId"}`, without contents. Other tabs fetch again and open only what changed. `vaultId` changes on reset, and a tab still holding the old key locks.

Other error codes: `vault_absent` 404, `vault_full` 413, `vault_unreadable` 500.

## PIN

Unlock with 6 digits, per browser. A million possibilities must not protect the key alone: if the hub stored the wrapped key, an agent that reads `<data>` would just try them all; if only the browser did, anyone who copies its storage could. So both are needed:

```
browser (localStorage nb.vault.pin):  salt, vault key wrapped by secret
hub (vault.json, pins):               random key per PIN, wrong-try count
proof  = Argon2id(PIN, salt)          same parameters as the master password
secret = HMAC-SHA256(hub key, proof)
```

- The hub hands over `secret` only for a correct `proof`, and counts the wrong ones. **5 wrong tries in a row: the PIN is gone**; the master password still works. The count is saved before the answer is sent, so a hub restart does not restore the allowance.
- The hub never sees the PIN, stores neither `secret` nor `proof`, and without the salt from the browser cannot test guesses. The browser has nothing that can be tested without the hub.
- Setting up a PIN asks for the master password, because the key that is currently unlocked cannot be read back.
- PINs such as `123456`, `111111`, `121212` are refused.
- **Changing the master password and reset turn off all PINs.** Other browsers find out on their next try. "Turn off everywhere" is for a lost browser: its own part no longer opens anything.

## Passkey

Unlock with a fingerprint, face or device PIN, per browser (vault menu → "Passkey unlock…"). A passkey with the **PRF** extension gives 32 secret bytes for a salt, only for this site and only after the person confirms. Those bytes wrap the vault key (AES-256-GCM, AAD `nextboard-vault/v1/passkey`).

```
browser (localStorage nb.vault.passkey):  credential id, salt, vault key wrapped by the PRF output
hub:                                      nothing
```

- No half on the hub, unlike the PIN: without the authenticator the record is useless, and the authenticator itself counts wrong guesses (user verification `required`).
- Setting it up asks for the master password, then `navigator.credentials.create` (RP id = the page's hostname) followed by `get` for the PRF output. An authenticator without PRF is refused with an explanation. Phones, Windows Hello, macOS and new security keys have PRF.
- **Only by name, over HTTPS with a valid certificate, or `localhost`.** An IP address (including `127.0.0.1`) and plain HTTP do not work, so the dialog explains this and the vault's gate does not offer it. A hub opened over `http://` keeps using the master password and PIN.
- **Changing the master password and reset turn off the passkey on that browser**, the same as the PIN.

## Filling forms

The key button in a browser tile ("Fill from Credentials"). **Always at the person's choice**, never when the page loads: a page that fills itself could collect without anyone seeing. Logins, cards and identities, each in its own tab. Never submits. Locked vault: the dialog asks for the PIN or password in place.

**Web tile (hub proxy).** A `fill` message to the bridge ([API-studio §4.5](API-studio.md)), which fills fields with the native setter and then sends `input` and `change`, so React and the like see it as typing.

- The site comes from the preview target, not from what the page reports. A login saved for another host needs a second click, because that is what phishing looks like. A match means the same host or a subdomain of it, and the same port when the address names a port. `localhost`, `127.0.0.1`, `::1` count as the same.
- Fields that cannot be seen are not filled: `display:none`, `opacity` near 0, off screen, size below 4 px.
- Two-step logins, TOTP codes (one field or a box per digit) and open shadow DOM are supported.
- **Cards and identities:** the `fill` message carries `fields`, values per HTML `autocomplete` token (`cc-name`, `cc-number`, `cc-csc`, `cc-exp-month`, `cc-exp-year`, `cc-exp`, `cc-type`; `given-name`, `additional-name`, `family-name`, `name`, `organization`, `email`, `tel`, `address-line1`–`3`, `address-level2` city, `address-level1` province, `postal-code`, `country-name`, `username`). The bridge first fills fields that name their token in `autocomplete` (`country` also for the country name), then the rest from name, id, placeholder, aria-label and label. A field is filled only once. A `<select>` is chosen by its value or its text: month `7` matches `07`, year `28` matches `2028`, `ID` matches `Indonesia`. `cc-exp` is used only when there are no month and year fields, `name` only when there are no first and last name fields. The answer is only how many fields were filled. Cards do not check the site, like card autofill in browsers: what decides is the person's click.

**Chrome on This PC, and other app tiles.** The hub cannot see inside them, so the values go into the focused field: username, Tab, password. Cards and identities there are filled piece by piece: the person clicks a field, then the Number, Expiry, Code, Name, Email, Address button and so on. The dialog names the window title and asks to check the address bar, because from here **the site cannot be confirmed**; the title is used only for sorting.

- Through that display's clipboard and then Ctrl+V, not typed: wayvnc silently drops characters that are not in its keymap (`$`, `ä`, `я`), and a password missing one letter is wrong without anyone seeing. A paste either succeeds whole or fails visibly. The display's clipboard is overwritten after about 400 ms, and does not come back to the user's browser clipboard.
- CDP is not used. `--remote-debugging-port` opens a local port that other users on a shared host could use to control a logged-in browser, and `--remote-debugging-pipe` closes Chrome when the hub restarts. `--load-extension` has been removed from branded Chrome.

## Limits

≤ 5000 records (items + folders), one blob ≤ 64 KB base64, the whole vault ≤ 64 MB, a batch ≤ 1000 items and a body ≤ 16 MB (the client splits per 500 items / 8 MB), item body 128 KB. ≤ 32 PINs; past that, the oldest is dropped.

## Storage

`<data>/vault.json` 0600, written whole and atomically on every change, saved first and only then in effect. The previous file stays as `vault.json.prev`. Read as corrupt: the corrupt file is moved to `vault.corrupt-<time>.json` and `.prev` is used. Both corrupt: the vault answers `vault_unreadable` and **refuses to write over it**; the hub keeps running, and only a reset gets it out.

Reset moves the file to `vault.reset-<time>.json` and does not delete it: if the password comes back to you, that file can still be opened (put it back as `vault.json`).

The file API already refuses `<data>`. A hub backup only needs to copy `vault.json`: its contents are safe as long as the master password is.

## Behavior in the browser

- **The key lives only in the tab's memory.** No localStorage or sessionStorage for anything secret. Reload, logout, `pagehide` and idle lock it. Each tab unlocks on its own. The key refuses to be used once locked, and open forms or dialogs go away with it and do not come back on unlock. An import in progress stops there; what was already sent stays.
- **Idle:** by default 15 minutes without touching a Credentials card or the header window (1 minute to 4 hours, per browser). Work in a console does not count, so hours in a terminal do not leave the vault open. Locking with the button locks every tab (`BroadcastChannel`).
- **Clipboard:** secrets are cleared from the clipboard 30 seconds after copying (adjustable, or "Never"), and when the vault locks. Browsers give no permission to read the clipboard without asking, so whatever was copied from another app in that time is cleared too; the settings dialog says so. Usernames and addresses are not cleared.
- **Hidden means not in the DOM:** a masked value is not rendered at all, and the mask does not follow its real length.
- **Master password** at least 8 characters (owner decision). A zxcvbn score below 3 only gets a warning and is not refused, even though whoever has `vault.json` can guess offline. A button in the form suggests 6 BIP-39 words (66 bits).
- **No outgoing requests:** no site icons (Bitwarden fetches them from an icon server, which leaks the domains). Only `http` and `https` become links, so a `javascript:` address from an import file stays text. The only exception is the leak check, and only on request (below).
- **Steam Guard codes:** a `steam://KEY` key (Bitwarden's way) or `otpauth://…&encoder=steam` (KeeWeb's way) produces a 5-character code from the Steam alphabet `23456789BCDFGHJKMNPQRTVWXY`, every 30 seconds. The code is shown whole, without spaces.
- **Password leak check:** the "Check for leaks" button on a login's page, or "Check passwords for leaks…" in the vault menu for all logins. Never automatic. The browser computes the password's SHA-1 (`@noble/hashes`, so it works on plain HTTP), then only its first 5 characters are sent to `GET /api/vault/pwned/{prefix}`. The hub forwards them to Have I Been Pwned's Pwned Passwords with `Add-Padding: true`, and all matching suffixes are compared in the browser. The hub never sees the password or its full hash. It goes through the hub so the page's CSP stays `connect-src 'self'`. The same password is asked about only once per check.

## Board and agents

The `credentials` node stores only `{"title": "…"}`. Boards are synced, snapshotted and readable by agents, so not a single secret is there. This card is on purpose **not** in `contentTiles` and `CONTENT_SOURCES` (it has no cable knob), `layoutNodeKinds`, or the planner's `node.add`; `internal/orch/credentials_test.go` guards this.

Deleting the card does not touch the vault, and every card on every board shows the same vault.

Agents can use an item only one way, described in the next section.

## Agents using a credential

Decision #40. The agent never receives a value. It asks the hub to run **one command** that gets the values from the vault, the owner approves it in the browser, and the output that comes back to the agent is already censored.

```
agent    credential_run{command, why, secrets}  →  hub: request "waiting", notification
browser  open item, check blob, send values      →  hub: run that command
hub      output with values replaced by ••••     →  agent
```

### Marking an item

- In the item form: **Agents may ask to use it**, then choose the projects. This choice is stored in the item's blob (`"agents": {"projects": […]}`), so the hub cannot change it.
- The browser also writes a **catalog** to `vault.json`: the item's name, type, projects and the names of its values, without the values. Unmarked items stay invisible, names included.
- Values that can be given: login → `username`, `password`; note → `note`; SSH key → `privateKey`, `publicKey`; every type → custom fields by their name. The built-in fields of cards and identities cannot, nor can the TOTP key.
- The catalog is only a hint for agents. At every unlock, the browser reconciles it with the blobs, and entries the blobs do not back are deleted.

### Agent tools

Consoles in a project that has marked items get three hub tools. A project without marked items gets no tools at all, so their definitions cost no tokens. The CLI reads its tool list when it starts, so a console that was already running before the first item was marked needs to be reopened.

| Tool | Contents |
|---|---|
| `credential_list` | Name, type, id and value names of the items for this project |
| `credential_run{command, why, secrets[{item, value, env}], stdin?{item, value}, timeout_ms?}` | Creates a request and waits ≤ 45 seconds. Done: the output. Not yet: an id for `credential_result` |
| `credential_result{id}` | Waits again ≤ 45 seconds. Only for the console that asked |

- Waiting is limited to 45 seconds because Codex cuts MCP calls off after 60 seconds.
- Claude Code and OpenCode do not ask permission for these three tools (`--allowedTools`, `permission`), because the approval is in the hub's dialog. Codex follows its own permission rules.
- Refused before reaching the owner: an item or value not in the project's catalog; an env name that is not a variable name or that changes shell behavior (`PATH`, `HOME`, `SHELL`, `IFS`, `ENV`, `BASH_ENV`, `LD_*`, `DYLD_*`, `NB_*` and the like); a duplicate env; no `why`; a command > 4000 bytes; > 8 values; and commands on the block list (decision #38).
- At most 3 waiting requests per console, 20 for the whole hub.

### Approval

- An `attention` notification with `kind: "credential"`, and a pill at the top of the page. The dialog shows the agent, project, machine and folder, the reason, the time limit, the exact command, and which value goes into which env.
- The dialog opens the item **from the blob**, not from the catalog. If the item is not marked for that project, is in the trash, or the value no longer exists, the **Allow once** button is disabled. A fake catalog entry opens nothing.
- Locked vault: the dialog asks for the PIN or master password in place.
- One approval holds for that command, once. Requests not answered within 10 minutes expire.

### Running

- The hub runs the command as its own process, on the console's machine and folder (the server for a `remoteTools` console), in a new login shell. Default time limit 2 minutes, at most 10.
- Values for env are sent through **stdin** to a preamble script that exports them: each byte is written as `\0ooo`, then restored with `printf %b`. Values are never on the command line, because `ps` on a server shows it to every user. Proven intact in sh, dash, bash, zsh and busybox, including quotes, `$`, newlines and unicode.
- The value for `stdin` is passed as is to the command's input.
- Values < 4 characters are refused, because they cannot be hidden without breaking the output.

### Censoring

Before reaching the agent, stdout and stderr get `••••` in every place that holds:

- the value, also without a trailing newline;
- each line ≥ 8 characters of a multi-line value;
- its base64 forms (four variants), hex (lowercase and uppercase), URL-encoded (query and path), and JSON string contents.

Output is kept up to 1 MB per stream. When it is cut off, the start of a value left at the end is dropped too. After that the output is cut to 30 000 characters, like the hub's `bash` tool.

### What is stored

- Requests exist only in memory: they are lost when the hub restarts, and the agent is asked to repeat. Values are in the hub's memory only for the duration of one run.
- `<data>/credential-log.jsonl` (0600, rotated to `.1` at 1 MB): who, when, command, item, value names, env, result, exit code, duration, and who approved. No values and no output.
- Event `credentialRequest` `{id, state}`, without contents.

### API

All through the ordinary login. Deleting an item deletes its entry, and a vault reset deletes the catalog.

| Route | Contents |
|---|---|
| `GET /api/vault/agents` | The catalog |
| `PUT /api/vault/agents/{id}` | `{name, type, projects, values}`; empty `projects` deletes it. The item must exist (404), the projects must exist (400) |
| `GET /api/vault/requests` | Requests that are waiting, running, or finished < 15 minutes ago |
| `POST /api/vault/requests/{id}/approve` | `{values}`, in the order of `uses`. 409 `request_decided`, 404 `request_not_found` |
| `POST /api/vault/requests/{id}/deny` | `{note?}` ≤ 300 characters, read out to the agent |
| `GET /api/vault/agent-log?limit=` | The log, newest first, ≤ 500 |

### Limits

**This protects against accidents, not against an agent that deliberately steals:**

- A command that runs a script in the project (`npm run`, `make`, `./deploy.sh`) runs that script's contents at that moment, and the agent can change them. What is approved is the command's name.
- While the command runs, an agent with Bash as the same user can read that process's env from `/proc`.
- Censoring does not recognize a value whose form was changed: reversed, cut or encrypted.
- Values travel over the network from the browser to the hub. On plain HTTP, anyone who can read the traffic can see them.
- The names of marked items are visible to that project's agents, and to anyone who reads `<data>`.

Filling browser forms for agents is not built. Agents cannot control a browser tile, so filling for them would be no different from the key button that already exists.

## Import and export

Import: unencrypted JSON and CSV from Bitwarden, CSV from Chrome and Firefox. Read and encrypted in the browser before sending. Bitwarden's encrypted export is refused with an explanation. Passkeys are not included.

Export: Bitwarden's JSON format without encryption, after the master password is entered again. The trash is not included.

## What it does not protect against

- **Forgotten master password = everything gone.** There is no recovery.
- **Plain HTTP.** Anyone who can alter the traffic between browser and hub can inject a script and capture the master password, then open the vault. The hub's login password travels the same path. Safe on localhost and inside Tailscale or a VPN; risky on shared Wi-Fi and across the internet without HTTPS.
- **Offline guessing.** An agent or anyone who reads `<data>` gets `vault.json` and can try to guess. Argon2id slows this down (about 0.3 seconds per guess per core), but the password's strength is what decides: a list of common passwords is done within minutes, and a weak 8-character password is among them.
- **The PIN, when both of its sides fall into the same hands.** Anyone who holds the browser's storage and the hub's folder can try a million PINs without the hub's count limit, about 3–4 days on one core and faster with many cores. This happens when the browser is on This PC, because an agent can read both.
- **Filling a form gives the secret away.** The page being filled can read the values; that is true of all autofill. Through the proxy, the password sent to the site passes through the hub's process. In This PC's Chrome, logged-in sessions are stored in its profile, and agents can read it. Do not save passwords in that Chrome (`--password-store=basic`); the dialog reminds you.
- **The app's own code.** An agent with Bash as the same user can replace the hub binary or the web files, then capture the master password at the next unlock. Encryption in the browser protects against reading files, not against someone who can replace the program. A self-hosted Bitwarden web vault has the same limit. For secrets a yolo agent must not reach, use another device.
- **While the vault is unlocked,** its contents are in the tab's memory, and on plain HTTP the key can also be read from there. XSS in the hub's app, even with a strict CSP, or a browser extension can read them.
- **The hub can return an old version.** Old blobs stay cryptographically valid, so a malicious hub can hide the latest changes. The same as Bitwarden.
- **Metadata the hub sees:** the number of records, blob sizes in steps of 512 bytes, and change times. For items marked for agents, also the name, type, projects and value names.
- **Agents using a credential** protects against accidents, not against an agent that deliberately steals (see its limits above).

## Not yet built

Storing other sites' passkeys in the vault (needs a browser extension that answers WebAuthn for that site), and the Credentials card on Android and desktop.
