# credential-stores

<!-- Generated from credential-stores.catalog.yaml (version 1). Do not edit: change the catalog, then run
     go test ./concepts -run TestCredentialStoresReferenceInSync -update (in pkg/asymptoteobserve) -->

Where credentials are kept and how they are got out: the files that hold them, by path, and the commands that hand them over. It is what a threat rule means when it says an agent read a credential.

A store is files or commands.

A path pattern names a file by the last components of its path, whatever directory they are
in: `.aws/credentials` is the file `credentials` in a directory `.aws`. Letters match without
regard to case, and `/` in a pattern is `/` or `\` in a path. `*` is any run of characters
within a component, `{a,b,}` one of the alternatives (which may be empty), a component `**`
any number of directories, and a trailing `/**` everything under a directory.

A command is matched after the command line is read as bash: by its name behind `sudo` and
the like, by words that follow each other somewhere after the name, and by an option of which
one must be there. `-w|-g` is either short option, alone or in a cluster such as `-aw`, in its
own case; a longer option is a whole word that may carry `=value`.

A `definite` store exists to hold a credential, or prints one. A `likely` one often holds one
and often does not. What two stores match is the definite one's, then the one listed first.

## Cloud provider credentials (`cloud`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `aws-shared-credentials`: AWS shared credentials file | definite | `.aws/credentials` |  | `/home/u/.aws/credentials` |
| `gcloud-application-default-credentials`: Google Cloud application default credentials | definite | `application_default_credentials.json` |  | `/home/u/.config/gcloud/application_default_credentials.json` |
| `gcloud-credential-db`: Google Cloud SDK credential and access token databases | definite | `.config/gcloud/{credentials,access_tokens}.db` |  | `/home/u/.config/gcloud/credentials.db` |
| `azure-cli-token-cache`: Azure CLI token caches | definite | `.azure/{msal_token_cache,accesstokens}.json` |  | `/home/u/.azure/msal_token_cache.json` |

## SSH keys and agents (`ssh`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `ssh-private-key`: SSH private key, by the name ssh-keygen gives it | definite | `id_{rsa,ed25519,ecdsa,dsa,ecdsa_sk,ed25519_sk}` |  | `/home/u/.ssh/id_rsa` |
| `ssh-key-file`: SSH private key under another name, in the SSH directory | definite | `.ssh/id_*` | `*.pub` | `/home/u/.ssh/id_github_deploy` |
| `ssh-agent-keys`: Keys loaded in the SSH agent, listed | likely | `ssh-add -l\|-L` |  | `ssh-add -l` |

## Environment files (`dotenv`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `dotenv`: Environment file | definite | `.env` |  | `/repo/.env` |
| `dotenv-variant`: Environment file for one environment or machine | likely | `.env.*` | `.env.{example,sample,template,dist}`<br>`.env.*.{example,sample,template,dist}` | `/repo/.env.production` |

## Source control credentials (`source-control`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `git-credential-store`: Git credential store | definite | `.git-credentials`<br>`.config/git/credentials` |  | `/home/u/.git-credentials` |
| `github-cli-hosts`: GitHub CLI host configuration, which holds its token unless a keyring does | likely | `.config/gh/hosts.yml` |  | `/home/u/.config/gh/hosts.yml` |

## Package and container registry credentials (`registry`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `npmrc`: npm configuration, which holds registry tokens when it holds any | likely | `.npmrc` |  | `/home/u/.npmrc` |
| `pypirc`: PyPI upload credentials | definite | `.pypirc` |  | `/home/u/.pypirc` |
| `docker-config`: Docker client configuration, which holds registry logins unless a credential helper does | likely | `.docker/config.json` |  | `/home/u/.docker/config.json` |

## Network and database client credentials (`network`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `netrc`: netrc machine logins | definite | `.netrc` |  | `/home/u/.netrc` |
| `pgpass`: PostgreSQL password file | definite | `.pgpass` |  | `/home/u/.pgpass` |

## Infrastructure state and provisioning credentials (`infrastructure`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `terraform-state`: Terraform state, which records resource secrets in clear text | likely | `*.tfstate`<br>`*.tfstate.backup` |  | `/repo/terraform.tfstate` |
| `terraform-cli-credentials`: Terraform CLI credentials | definite | `.terraform.d/credentials.tfrc.json` |  | `/home/u/.terraform.d/credentials.tfrc.json` |

## Files named for what they hold (`generic`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `credentials-file`: A file named credentials | likely | `credentials`<br>`credentials.{json,toml}` |  | `/srv/app/credentials` |
| `secrets-file`: A data file named secrets | likely | `secrets.{yaml,yml,json}` |  | `/repo/config/secrets.yaml` |

## Kubernetes credentials (`kubernetes`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `kubeconfig`: Kubernetes client configuration, which holds cluster credentials or names where they are | likely | `.kube/config`<br>`.kube/config-*`<br>`kubeconfig`<br>`kubeconfig.{yaml,yml}`<br>`.kube/*kubeconfig*` |  | `/home/u/.kube/config` |
| `kubernetes-token-file`: A file named for a Kubernetes or cluster token | likely | `{sa,kube,kubernetes,k8s,cluster,admin,user,deploy,bootstrap}{-,_,}token{,.csv}`<br>`service{-,_,}account{-,_,}token{,.csv}`<br>`.kube/**/*token*` |  | `/etc/kubernetes/admin-token` |
| `kubernetes-service-account-token`: Mounted Kubernetes service account token | definite | `var/run/secrets/kubernetes.io/serviceaccount/token` |  | `/var/run/secrets/kubernetes.io/serviceaccount/token` |

## GnuPG keyrings (`gpg`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `gpg-private-keys`: GnuPG private keys | definite | `.gnupg/private-keys-v1.d/**`<br>`secring.gpg` |  | `/home/u/.gnupg/private-keys-v1.d/0A1B2C3D.key` |
| `gpg-key-material`: GnuPG revocation certificates and exported key files | likely | `.gnupg/openpgp-revocs.d/**`<br>`.gnupg/*.key` |  | `/home/u/.gnupg/openpgp-revocs.d/0A1B2C3D.rev` |
| `gpg-secret-key-export`: GnuPG secret keys exported | definite | `gpg{,2} --export-secret-k*\|--export-secret-sub*` |  | `gpg --export-secret-keys --armor 0xDEADBEEF` |

## Password manager stores (`password-manager`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `keepass-database`: KeePass database | definite | `*.kdbx` |  | `/home/u/Documents/Passwords.kdbx` |
| `pass-store`: pass password store | definite | `.password-store/**` |  | `/home/u/.password-store/work/github.gpg` |
| `password-manager-data`: 1Password, Bitwarden and LastPass local data | likely | `{.1password,1Password}/**`<br>`.config/Bitwarden/**`<br>`{.lastpass,LastPass}/**`<br>`.bitwarden/**`<br>`.config/Bitwarden*.sqlite`<br>`.config/Bitwarden*.sqlite.*` |  | `/home/u/.1password/agent.sock` |

## Browser session stores (`browser`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `firefox-session-store`: Firefox cookies and saved logins | definite | `cookies.sqlite`<br>`logins.json` |  | `/home/u/.mozilla/firefox/abc123.default-release/cookies.sqlite` |
| `browser-profile-session-store`: Cookies and saved logins in a browser profile | definite | `{google-chrome,chromium,chrome,brave-browser,brave browser,bravebrowser,bravesoftware,microsoft-edge,microsoft edge,microsoftedge,edge,firefox,mozilla,safari}{,-beta, beta,-dev, dev,-canary, canary,-unstable, unstable,-sxs, sxs}/**/{*cookies,*login data}` |  | `/home/u/.config/google-chrome/Default/Cookies` |

## Operating system keychains and secret services (`keychain`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `macos-keychain`: macOS keychain passwords, printed | definite | `security find-{generic,internet}-password -w\|-g`<br>`security dump-keychain -d` |  | `security find-generic-password -s 'GitHub' -w` |
| `linux-secret-service`: Secret Service and KWallet secrets, looked up | definite | `secret-tool {lookup,search}`<br>`kwallet-query -r\|--read-password` |  | `secret-tool lookup service github` |
| `windows-credential-manager`: Windows Credential Manager and SecretManagement secrets, printed | definite | `vaultcmd{,.exe} /listcreds*`<br>`get-storedcredential`<br>`get-secret -asplaintext` |  | `vaultcmd /listcreds:"Windows Credentials" /all` |

## Tokens a signed-in CLI prints (`cli-token`)

| Store | Tier | Paths or commands | Except | Example |
|---|---|---|---|---|
| `gcloud-token`: Google Cloud access or identity token | definite | `gcloud auth print-{access,identity}-token`<br>`gcloud auth application-default print-{access,identity}-token` |  | `gcloud auth print-access-token` |
| `github-cli-token`: GitHub CLI token | definite | `gh auth token` |  | `gh auth token` |
| `aws-credential-export`: AWS credentials exported by the CLI | definite | `aws configure export-credentials` |  | `aws configure export-credentials --format env` |
| `azure-cli-token`: Azure access token | definite | `az account get-access-token` |  | `az account get-access-token --resource https://management.azure.com` |
| `vault-token`: Vault token | definite | `vault print token` |  | `vault print token` |
| `heroku-token`: Heroku API token | definite | `heroku auth:token` |  | `heroku auth:token` |
| `doctl-token`: DigitalOcean API token | definite | `doctl auth token` |  | `doctl auth token` |
| `kubectl-token`: Kubernetes service account token, minted | definite | `kubectl create token` |  | `kubectl create token default -n kube-system` |

## Generated expressions

What a rule or a query uses is a selection of the catalog as expressions. These are the two
selections of every kind: the files over a path `p` in CEL and a column `FilePath` in KQL, and
the commands over a parsed command `c` in CEL, which reads `c.name` and `c.args`. The regular
expressions are RE2 syntax, which CEL and KQL read the same way. Commands have no KQL form: a
query over raw events has no parsed command line.

Two more forms read a text `s` where there is no path or parsed command to ask. The files a
text names: a command line that does not parse, a prompt handed to another program. A file
is named by the characters a name is written with, a word that is also a file name
(`credentials`) counts only after a directory, and a store that excludes some names is not
named by a text that also holds one of those. And the commands as a log holds them when
redaction took the command's name: `TOKEN=$(gh auth token)` is logged as
`TOKEN=[REDACTED] auth token)`. A command told only by its name, by one plain word or by
one-letter options has no such form.

A rule of a pack that ships this catalog calls it by name instead of carrying the
expressions:

- `credential_path(p)` is true for a path that is a store of files, of either tier. `credential_path(p, "definite")` takes the
  definite stores only, and kinds after the tier take those kinds only:
  `credential_path(p, "likely", "cloud")`.
- `credential_command(c)` is true for a parsed command that reads a store of commands, of either tier. `credential_command(c, "definite")` takes the
  definite stores only, and kinds after the tier take those kinds only:
  `credential_command(c, "likely", "cloud")`.
- `credential_mention(s)` is true for a text that names a store of files, of either tier. `credential_mention(s, "definite")` takes the
  definite stores only, and kinds after the tier take those kinds only:
  `credential_mention(s, "likely", "cloud")`.
- `credential_command_redacted(s)` is true for a command line in which redaction took the name of a command that reads a store of commands, of either tier. `credential_command_redacted(s, "definite")` takes the
  definite stores only, and kinds after the tier take those kinds only:
  `credential_command_redacted(s, "likely", "cloud")`.

### `definite` stores

Files, CEL:

```
(p.matches("(?i)(?:^|[/\\\\])(?:\\.aws[/\\\\]credentials$|application_default_credentials\\.json$|\\.config[/\\\\]gcloud[/\\\\](?:credentials|access_tokens)\\.db$|\\.azure[/\\\\](?:msal_token_cache|accesstokens)\\.json$|id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)$|\\.env$|\\.git-credentials$|\\.config[/\\\\]git[/\\\\]credentials$|\\.pypirc$|\\.netrc$|\\.pgpass$|\\.terraform\\.d[/\\\\]credentials\\.tfrc\\.json$|var[/\\\\]run[/\\\\]secrets[/\\\\]kubernetes\\.io[/\\\\]serviceaccount[/\\\\]token$|\\.gnupg[/\\\\]private-keys-v1\\.d[/\\\\]|secring\\.gpg$|[^/\\\\]*\\.kdbx$|\\.password-store[/\\\\]|cookies\\.sqlite$|logins\\.json$|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\\\](?:[^/\\\\]+[/\\\\])*(?:[^/\\\\]*cookies|[^/\\\\]*login data)$)") || (p.matches("(?i)(?:^|[/\\\\])(?:\\.ssh[/\\\\]id_[^/\\\\]*$)") && !p.matches("(?i)(?:^|[/\\\\])(?:[^/\\\\]*\\.pub$)")))
```

Files, KQL:

```
(FilePath matches regex @"(?i)(?:^|[/\\])(?:\.aws[/\\]credentials$|application_default_credentials\.json$|\.config[/\\]gcloud[/\\](?:credentials|access_tokens)\.db$|\.azure[/\\](?:msal_token_cache|accesstokens)\.json$|id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)$|\.env$|\.git-credentials$|\.config[/\\]git[/\\]credentials$|\.pypirc$|\.netrc$|\.pgpass$|\.terraform\.d[/\\]credentials\.tfrc\.json$|var[/\\]run[/\\]secrets[/\\]kubernetes\.io[/\\]serviceaccount[/\\]token$|\.gnupg[/\\]private-keys-v1\.d[/\\]|secring\.gpg$|[^/\\]*\.kdbx$|\.password-store[/\\]|cookies\.sqlite$|logins\.json$|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\](?:[^/\\]+[/\\])*(?:[^/\\]*cookies|[^/\\]*login data)$)" or (FilePath matches regex @"(?i)(?:^|[/\\])(?:\.ssh[/\\]id_[^/\\]*$)" and not(FilePath matches regex @"(?i)(?:^|[/\\])(?:[^/\\]*\.pub$)")))
```

Commands, CEL:

```
(c.name.matches("(?i)^(?:gpg(?:|2)|security|secret-tool|kwallet-query|vaultcmd(?:|\\.exe)|get-storedcredential|get-secret|gcloud|gh|aws|az|vault|heroku|doctl|kubectl)$") && ((c.name.matches("(?i)^gpg(?:|2)$") && c.args.matches("(?m)^(?:(?i:--export-secret-k[^\\n]*|--export-secret-sub[^\\n]*)(?:=[^\\n]*)?)$")) || (c.name.matches("(?i)^security$") && c.args.matches("(?im)^find-(?:generic|internet)-password$") && c.args.matches("(?m)^(?:-[A-Za-z0-9]*[wg][A-Za-z0-9]*)$")) || (c.name.matches("(?i)^security$") && c.args.matches("(?im)^dump-keychain$") && c.args.matches("(?m)^(?:-[A-Za-z0-9]*[d][A-Za-z0-9]*)$")) || (c.name.matches("(?i)^secret-tool$") && c.args.matches("(?im)^(?:lookup|search)$")) || (c.name.matches("(?i)^kwallet-query$") && c.args.matches("(?m)^(?:-[A-Za-z0-9]*[r][A-Za-z0-9]*|(?i:--read-password)(?:=[^\\n]*)?)$")) || (c.name.matches("(?i)^vaultcmd(?:|\\.exe)$") && c.args.matches("(?im)^/listcreds[^\\n]*$")) || c.name.matches("(?i)^get-storedcredential$") || (c.name.matches("(?i)^get-secret$") && c.args.matches("(?m)^(?:(?i:-asplaintext)(?:=[^\\n]*)?)$")) || (c.name.matches("(?i)^gcloud$") && c.args.matches("(?im)^auth\\nprint-(?:access|identity)-token$")) || (c.name.matches("(?i)^gcloud$") && c.args.matches("(?im)^auth\\napplication-default\\nprint-(?:access|identity)-token$")) || (c.name.matches("(?i)^gh$") && c.args.matches("(?im)^auth\\ntoken$")) || (c.name.matches("(?i)^aws$") && c.args.matches("(?im)^configure\\nexport-credentials$")) || (c.name.matches("(?i)^az$") && c.args.matches("(?im)^account\\nget-access-token$")) || (c.name.matches("(?i)^vault$") && c.args.matches("(?im)^print\\ntoken$")) || (c.name.matches("(?i)^heroku$") && c.args.matches("(?im)^auth:token$")) || (c.name.matches("(?i)^doctl$") && c.args.matches("(?im)^auth\\ntoken$")) || (c.name.matches("(?i)^kubectl$") && c.args.matches("(?im)^create\\ntoken$"))))
```

Files named in a text, CEL:

```
(s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:\\.aws[/\\\\]credentials\\.?(?:$|[^\\w.~%+/\\\\-])|application_default_credentials\\.json\\.?(?:$|[^\\w.~%+/\\\\-])|\\.config[/\\\\]gcloud[/\\\\](?:credentials|access_tokens)\\.db\\.?(?:$|[^\\w.~%+/\\\\-])|\\.azure[/\\\\](?:msal_token_cache|accesstokens)\\.json\\.?(?:$|[^\\w.~%+/\\\\-])|\\.env\\.?(?:$|[^\\w.~%+/\\\\-])|\\.git-credentials\\.?(?:$|[^\\w.~%+/\\\\-])|\\.config[/\\\\]git[/\\\\]credentials\\.?(?:$|[^\\w.~%+/\\\\-])|\\.pypirc\\.?(?:$|[^\\w.~%+/\\\\-])|\\.netrc\\.?(?:$|[^\\w.~%+/\\\\-])|\\.pgpass\\.?(?:$|[^\\w.~%+/\\\\-])|\\.terraform\\.d[/\\\\]credentials\\.tfrc\\.json\\.?(?:$|[^\\w.~%+/\\\\-])|var[/\\\\]run[/\\\\]secrets[/\\\\]kubernetes\\.io[/\\\\]serviceaccount[/\\\\]token\\.?(?:$|[^\\w.~%+/\\\\-])|\\.gnupg[/\\\\]private-keys-v1\\.d[/\\\\]|secring\\.gpg\\.?(?:$|[^\\w.~%+/\\\\-])|[\\w.~%+-]*\\.kdbx\\.?(?:$|[^\\w.~%+/\\\\-])|\\.password-store[/\\\\]|cookies\\.sqlite\\.?(?:$|[^\\w.~%+/\\\\-])|logins\\.json\\.?(?:$|[^\\w.~%+/\\\\-])|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\\\](?:[^/\\\\\\s'\"]+[/\\\\])*(?:[\\w.~%+-]*cookies|[\\w.~%+-]*login data)\\.?(?:$|[^\\w.~%+/\\\\-]))|[/\\\\](?:id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)\\.?(?:$|[^\\w.~%+/\\\\-])))") || (s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:\\.ssh[/\\\\]id_[\\w.~%+-]*\\.?(?:$|[^\\w.~%+/\\\\-])))") && !s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:[\\w.~%+-]*\\.pub\\.?(?:$|[^\\w.~%+/\\\\-])))")))
```

Commands with the name redacted, CEL:

```
s.matches("(?i)(?:\\[REDACTED\\]\\s+(?:-[^\\s;&|'\"()]*\\s+)*(?:(?:--export-secret-k[^\\s;&|'\"()]*|--export-secret-sub[^\\s;&|'\"()]*)(?:=[^\\s;&|'\"()]*)?)(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?find-(?:generic|internet)-password(?:$|[\\s;&|)'\"])(?:[^;&|\\n]*\\s)?(?:(?-i:-[A-Za-z0-9]*[wg][A-Za-z0-9]*))(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?dump-keychain(?:$|[\\s;&|)'\"])(?:[^;&|\\n]*\\s)?(?:(?-i:-[A-Za-z0-9]*[d][A-Za-z0-9]*))(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:-[^\\s;&|'\"()]*\\s+)*(?:(?:--read-password)(?:=[^\\s;&|'\"()]*)?)(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?/listcreds[^\\s;&|'\"()]*(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:-[^\\s;&|'\"()]*\\s+)*(?:(?:-asplaintext)(?:=[^\\s;&|'\"()]*)?)(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth\\s+print-(?:access|identity)-token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth\\s+application-default\\s+print-(?:access|identity)-token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth\\s+token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?configure\\s+export-credentials(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?account\\s+get-access-token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?print\\s+token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth:token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth\\s+token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?create\\s+token(?:$|[\\s;&|)'\"]))")
```

### `likely` and `definite` stores

Files, CEL:

```
(p.matches("(?i)(?:^|[/\\\\])(?:\\.aws[/\\\\]credentials$|application_default_credentials\\.json$|\\.config[/\\\\]gcloud[/\\\\](?:credentials|access_tokens)\\.db$|\\.azure[/\\\\](?:msal_token_cache|accesstokens)\\.json$|id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)$|\\.env$|\\.git-credentials$|\\.config[/\\\\]git[/\\\\]credentials$|\\.config[/\\\\]gh[/\\\\]hosts\\.yml$|\\.npmrc$|\\.pypirc$|\\.docker[/\\\\]config\\.json$|\\.netrc$|\\.pgpass$|[^/\\\\]*\\.tfstate$|[^/\\\\]*\\.tfstate\\.backup$|\\.terraform\\.d[/\\\\]credentials\\.tfrc\\.json$|credentials$|credentials\\.(?:json|toml)$|secrets\\.(?:yaml|yml|json)$|\\.kube[/\\\\]config$|\\.kube[/\\\\]config-[^/\\\\]*$|kubeconfig$|kubeconfig\\.(?:yaml|yml)$|\\.kube[/\\\\][^/\\\\]*kubeconfig[^/\\\\]*$|(?:sa|kube|kubernetes|k8s|cluster|admin|user|deploy|bootstrap)(?:-|_|)token(?:|\\.csv)$|service(?:-|_|)account(?:-|_|)token(?:|\\.csv)$|\\.kube[/\\\\](?:[^/\\\\]+[/\\\\])*[^/\\\\]*token[^/\\\\]*$|var[/\\\\]run[/\\\\]secrets[/\\\\]kubernetes\\.io[/\\\\]serviceaccount[/\\\\]token$|\\.gnupg[/\\\\]private-keys-v1\\.d[/\\\\]|secring\\.gpg$|\\.gnupg[/\\\\]openpgp-revocs\\.d[/\\\\]|\\.gnupg[/\\\\][^/\\\\]*\\.key$|[^/\\\\]*\\.kdbx$|\\.password-store[/\\\\]|(?:\\.1password|1Password)[/\\\\]|\\.config[/\\\\]Bitwarden[/\\\\]|(?:\\.lastpass|LastPass)[/\\\\]|\\.bitwarden[/\\\\]|\\.config[/\\\\]Bitwarden[^/\\\\]*\\.sqlite$|\\.config[/\\\\]Bitwarden[^/\\\\]*\\.sqlite\\.[^/\\\\]*$|cookies\\.sqlite$|logins\\.json$|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\\\](?:[^/\\\\]+[/\\\\])*(?:[^/\\\\]*cookies|[^/\\\\]*login data)$)") || (p.matches("(?i)(?:^|[/\\\\])(?:\\.ssh[/\\\\]id_[^/\\\\]*$)") && !p.matches("(?i)(?:^|[/\\\\])(?:[^/\\\\]*\\.pub$)")) || (p.matches("(?i)(?:^|[/\\\\])(?:\\.env\\.[^/\\\\]*$)") && !p.matches("(?i)(?:^|[/\\\\])(?:\\.env\\.(?:example|sample|template|dist)$|\\.env\\.[^/\\\\]*\\.(?:example|sample|template|dist)$)")))
```

Files, KQL:

```
(FilePath matches regex @"(?i)(?:^|[/\\])(?:\.aws[/\\]credentials$|application_default_credentials\.json$|\.config[/\\]gcloud[/\\](?:credentials|access_tokens)\.db$|\.azure[/\\](?:msal_token_cache|accesstokens)\.json$|id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)$|\.env$|\.git-credentials$|\.config[/\\]git[/\\]credentials$|\.config[/\\]gh[/\\]hosts\.yml$|\.npmrc$|\.pypirc$|\.docker[/\\]config\.json$|\.netrc$|\.pgpass$|[^/\\]*\.tfstate$|[^/\\]*\.tfstate\.backup$|\.terraform\.d[/\\]credentials\.tfrc\.json$|credentials$|credentials\.(?:json|toml)$|secrets\.(?:yaml|yml|json)$|\.kube[/\\]config$|\.kube[/\\]config-[^/\\]*$|kubeconfig$|kubeconfig\.(?:yaml|yml)$|\.kube[/\\][^/\\]*kubeconfig[^/\\]*$|(?:sa|kube|kubernetes|k8s|cluster|admin|user|deploy|bootstrap)(?:-|_|)token(?:|\.csv)$|service(?:-|_|)account(?:-|_|)token(?:|\.csv)$|\.kube[/\\](?:[^/\\]+[/\\])*[^/\\]*token[^/\\]*$|var[/\\]run[/\\]secrets[/\\]kubernetes\.io[/\\]serviceaccount[/\\]token$|\.gnupg[/\\]private-keys-v1\.d[/\\]|secring\.gpg$|\.gnupg[/\\]openpgp-revocs\.d[/\\]|\.gnupg[/\\][^/\\]*\.key$|[^/\\]*\.kdbx$|\.password-store[/\\]|(?:\.1password|1Password)[/\\]|\.config[/\\]Bitwarden[/\\]|(?:\.lastpass|LastPass)[/\\]|\.bitwarden[/\\]|\.config[/\\]Bitwarden[^/\\]*\.sqlite$|\.config[/\\]Bitwarden[^/\\]*\.sqlite\.[^/\\]*$|cookies\.sqlite$|logins\.json$|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\](?:[^/\\]+[/\\])*(?:[^/\\]*cookies|[^/\\]*login data)$)" or (FilePath matches regex @"(?i)(?:^|[/\\])(?:\.ssh[/\\]id_[^/\\]*$)" and not(FilePath matches regex @"(?i)(?:^|[/\\])(?:[^/\\]*\.pub$)")) or (FilePath matches regex @"(?i)(?:^|[/\\])(?:\.env\.[^/\\]*$)" and not(FilePath matches regex @"(?i)(?:^|[/\\])(?:\.env\.(?:example|sample|template|dist)$|\.env\.[^/\\]*\.(?:example|sample|template|dist)$)")))
```

Commands, CEL:

```
(c.name.matches("(?i)^(?:ssh-add|gpg(?:|2)|security|secret-tool|kwallet-query|vaultcmd(?:|\\.exe)|get-storedcredential|get-secret|gcloud|gh|aws|az|vault|heroku|doctl|kubectl)$") && ((c.name.matches("(?i)^ssh-add$") && c.args.matches("(?m)^(?:-[A-Za-z0-9]*[lL][A-Za-z0-9]*)$")) || (c.name.matches("(?i)^gpg(?:|2)$") && c.args.matches("(?m)^(?:(?i:--export-secret-k[^\\n]*|--export-secret-sub[^\\n]*)(?:=[^\\n]*)?)$")) || (c.name.matches("(?i)^security$") && c.args.matches("(?im)^find-(?:generic|internet)-password$") && c.args.matches("(?m)^(?:-[A-Za-z0-9]*[wg][A-Za-z0-9]*)$")) || (c.name.matches("(?i)^security$") && c.args.matches("(?im)^dump-keychain$") && c.args.matches("(?m)^(?:-[A-Za-z0-9]*[d][A-Za-z0-9]*)$")) || (c.name.matches("(?i)^secret-tool$") && c.args.matches("(?im)^(?:lookup|search)$")) || (c.name.matches("(?i)^kwallet-query$") && c.args.matches("(?m)^(?:-[A-Za-z0-9]*[r][A-Za-z0-9]*|(?i:--read-password)(?:=[^\\n]*)?)$")) || (c.name.matches("(?i)^vaultcmd(?:|\\.exe)$") && c.args.matches("(?im)^/listcreds[^\\n]*$")) || c.name.matches("(?i)^get-storedcredential$") || (c.name.matches("(?i)^get-secret$") && c.args.matches("(?m)^(?:(?i:-asplaintext)(?:=[^\\n]*)?)$")) || (c.name.matches("(?i)^gcloud$") && c.args.matches("(?im)^auth\\nprint-(?:access|identity)-token$")) || (c.name.matches("(?i)^gcloud$") && c.args.matches("(?im)^auth\\napplication-default\\nprint-(?:access|identity)-token$")) || (c.name.matches("(?i)^gh$") && c.args.matches("(?im)^auth\\ntoken$")) || (c.name.matches("(?i)^aws$") && c.args.matches("(?im)^configure\\nexport-credentials$")) || (c.name.matches("(?i)^az$") && c.args.matches("(?im)^account\\nget-access-token$")) || (c.name.matches("(?i)^vault$") && c.args.matches("(?im)^print\\ntoken$")) || (c.name.matches("(?i)^heroku$") && c.args.matches("(?im)^auth:token$")) || (c.name.matches("(?i)^doctl$") && c.args.matches("(?im)^auth\\ntoken$")) || (c.name.matches("(?i)^kubectl$") && c.args.matches("(?im)^create\\ntoken$"))))
```

Files named in a text, CEL:

```
(s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:\\.aws[/\\\\]credentials\\.?(?:$|[^\\w.~%+/\\\\-])|application_default_credentials\\.json\\.?(?:$|[^\\w.~%+/\\\\-])|\\.config[/\\\\]gcloud[/\\\\](?:credentials|access_tokens)\\.db\\.?(?:$|[^\\w.~%+/\\\\-])|\\.azure[/\\\\](?:msal_token_cache|accesstokens)\\.json\\.?(?:$|[^\\w.~%+/\\\\-])|\\.env\\.?(?:$|[^\\w.~%+/\\\\-])|\\.git-credentials\\.?(?:$|[^\\w.~%+/\\\\-])|\\.config[/\\\\]git[/\\\\]credentials\\.?(?:$|[^\\w.~%+/\\\\-])|\\.config[/\\\\]gh[/\\\\]hosts\\.yml\\.?(?:$|[^\\w.~%+/\\\\-])|\\.npmrc\\.?(?:$|[^\\w.~%+/\\\\-])|\\.pypirc\\.?(?:$|[^\\w.~%+/\\\\-])|\\.docker[/\\\\]config\\.json\\.?(?:$|[^\\w.~%+/\\\\-])|\\.netrc\\.?(?:$|[^\\w.~%+/\\\\-])|\\.pgpass\\.?(?:$|[^\\w.~%+/\\\\-])|[\\w.~%+-]*\\.tfstate\\.?(?:$|[^\\w.~%+/\\\\-])|[\\w.~%+-]*\\.tfstate\\.backup\\.?(?:$|[^\\w.~%+/\\\\-])|\\.terraform\\.d[/\\\\]credentials\\.tfrc\\.json\\.?(?:$|[^\\w.~%+/\\\\-])|credentials\\.(?:json|toml)\\.?(?:$|[^\\w.~%+/\\\\-])|secrets\\.(?:yaml|yml|json)\\.?(?:$|[^\\w.~%+/\\\\-])|\\.kube[/\\\\]config\\.?(?:$|[^\\w.~%+/\\\\-])|\\.kube[/\\\\]config-[\\w.~%+-]*\\.?(?:$|[^\\w.~%+/\\\\-])|kubeconfig\\.(?:yaml|yml)\\.?(?:$|[^\\w.~%+/\\\\-])|\\.kube[/\\\\][\\w.~%+-]*kubeconfig[\\w.~%+-]*\\.?(?:$|[^\\w.~%+/\\\\-])|(?:sa|kube|kubernetes|k8s|cluster|admin|user|deploy|bootstrap)(?:-|_|)token(?:|\\.csv)\\.?(?:$|[^\\w.~%+/\\\\-])|service(?:-|_|)account(?:-|_|)token(?:|\\.csv)\\.?(?:$|[^\\w.~%+/\\\\-])|\\.kube[/\\\\](?:[^/\\\\\\s'\"]+[/\\\\])*[\\w.~%+-]*token[\\w.~%+-]*\\.?(?:$|[^\\w.~%+/\\\\-])|var[/\\\\]run[/\\\\]secrets[/\\\\]kubernetes\\.io[/\\\\]serviceaccount[/\\\\]token\\.?(?:$|[^\\w.~%+/\\\\-])|\\.gnupg[/\\\\]private-keys-v1\\.d[/\\\\]|secring\\.gpg\\.?(?:$|[^\\w.~%+/\\\\-])|\\.gnupg[/\\\\]openpgp-revocs\\.d[/\\\\]|\\.gnupg[/\\\\][\\w.~%+-]*\\.key\\.?(?:$|[^\\w.~%+/\\\\-])|[\\w.~%+-]*\\.kdbx\\.?(?:$|[^\\w.~%+/\\\\-])|\\.password-store[/\\\\]|(?:\\.1password|1Password)[/\\\\]|\\.config[/\\\\]Bitwarden[/\\\\]|(?:\\.lastpass|LastPass)[/\\\\]|\\.bitwarden[/\\\\]|\\.config[/\\\\]Bitwarden[\\w.~%+-]*\\.sqlite\\.?(?:$|[^\\w.~%+/\\\\-])|\\.config[/\\\\]Bitwarden[\\w.~%+-]*\\.sqlite\\.[\\w.~%+-]*\\.?(?:$|[^\\w.~%+/\\\\-])|cookies\\.sqlite\\.?(?:$|[^\\w.~%+/\\\\-])|logins\\.json\\.?(?:$|[^\\w.~%+/\\\\-])|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\\\](?:[^/\\\\\\s'\"]+[/\\\\])*(?:[\\w.~%+-]*cookies|[\\w.~%+-]*login data)\\.?(?:$|[^\\w.~%+/\\\\-]))|[/\\\\](?:id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)\\.?(?:$|[^\\w.~%+/\\\\-])|credentials\\.?(?:$|[^\\w.~%+/\\\\-])|kubeconfig\\.?(?:$|[^\\w.~%+/\\\\-])))") || (s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:\\.ssh[/\\\\]id_[\\w.~%+-]*\\.?(?:$|[^\\w.~%+/\\\\-])))") && !s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:[\\w.~%+-]*\\.pub\\.?(?:$|[^\\w.~%+/\\\\-])))")) || (s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:\\.env\\.[\\w.~%+-]*\\.?(?:$|[^\\w.~%+/\\\\-])))") && !s.matches("(?i)(?:(?:^|[^\\w.~%+-])(?:\\.env\\.(?:example|sample|template|dist)\\.?(?:$|[^\\w.~%+/\\\\-])|\\.env\\.[\\w.~%+-]*\\.(?:example|sample|template|dist)\\.?(?:$|[^\\w.~%+/\\\\-])))")))
```

Commands with the name redacted, CEL:

```
s.matches("(?i)(?:\\[REDACTED\\]\\s+(?:-[^\\s;&|'\"()]*\\s+)*(?:(?:--export-secret-k[^\\s;&|'\"()]*|--export-secret-sub[^\\s;&|'\"()]*)(?:=[^\\s;&|'\"()]*)?)(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?find-(?:generic|internet)-password(?:$|[\\s;&|)'\"])(?:[^;&|\\n]*\\s)?(?:(?-i:-[A-Za-z0-9]*[wg][A-Za-z0-9]*))(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?dump-keychain(?:$|[\\s;&|)'\"])(?:[^;&|\\n]*\\s)?(?:(?-i:-[A-Za-z0-9]*[d][A-Za-z0-9]*))(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:-[^\\s;&|'\"()]*\\s+)*(?:(?:--read-password)(?:=[^\\s;&|'\"()]*)?)(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?/listcreds[^\\s;&|'\"()]*(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:-[^\\s;&|'\"()]*\\s+)*(?:(?:-asplaintext)(?:=[^\\s;&|'\"()]*)?)(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth\\s+print-(?:access|identity)-token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth\\s+application-default\\s+print-(?:access|identity)-token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth\\s+token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?configure\\s+export-credentials(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?account\\s+get-access-token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?print\\s+token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth:token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?auth\\s+token(?:$|[\\s;&|)'\"])|\\[REDACTED\\]\\s+(?:[^;&|\\n'\"]*\\s)?create\\s+token(?:$|[\\s;&|)'\"]))")
```
