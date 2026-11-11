# credential-stores

<!-- Generated from credential-stores.yaml (version 1). Do not edit: change the catalog, then run
     go test ./concepts -run TestCredentialStoresReferenceInSync -update (in pkg/asymptoteobserve) -->

The files that hold credentials, by path: what a threat rule means when it says an agent read a credential.

A pattern names a file by the last components of its path, whatever directory they are in:
`.aws/credentials` is the file `credentials` in a directory `.aws`. Letters match without regard
to case, and `/` in a pattern is `/` or `\` in a path. `*` is any run of characters within a
component, `{a,b,}` one of the alternatives (which may be empty), a component `**` any number
of directories, and a trailing `/**` everything under a directory.

A `definite` store exists to hold a credential. A `likely` one often holds one and often does
not. A path that two stores match is the definite one's, then the one listed first.

## Cloud provider credentials (`cloud`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `aws-shared-credentials`: AWS shared credentials file | definite | `.aws/credentials` |  | `/home/u/.aws/credentials` |
| `gcloud-application-default-credentials`: Google Cloud application default credentials | definite | `application_default_credentials.json` |  | `/home/u/.config/gcloud/application_default_credentials.json` |
| `gcloud-credential-db`: Google Cloud SDK credential and access token databases | definite | `.config/gcloud/{credentials,access_tokens}.db` |  | `/home/u/.config/gcloud/credentials.db` |
| `azure-cli-token-cache`: Azure CLI token caches | definite | `.azure/{msal_token_cache,accesstokens}.json` |  | `/home/u/.azure/msal_token_cache.json` |

## SSH keys (`ssh`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `ssh-private-key`: SSH private key | definite | `id_{rsa,ed25519,ecdsa,dsa,ecdsa_sk,ed25519_sk}`<br>`.ssh/id_*` | `*.pub` | `/home/u/.ssh/id_rsa` |

## Environment files (`dotenv`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `dotenv`: Environment file | definite | `.env` |  | `/repo/.env` |
| `dotenv-variant`: Environment file for one environment or machine | likely | `.env.*` | `.env.{example,sample,template,dist}`<br>`.env.*.{example,sample,template,dist}` | `/repo/.env.production` |

## Source control credentials (`source-control`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `git-credential-store`: Git credential store | definite | `.git-credentials`<br>`.config/git/credentials` |  | `/home/u/.git-credentials` |
| `github-cli-hosts`: GitHub CLI host configuration, which holds its token unless a keyring does | likely | `.config/gh/hosts.yml` |  | `/home/u/.config/gh/hosts.yml` |

## Package and container registry credentials (`registry`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `npmrc`: npm configuration, which holds registry tokens when it holds any | likely | `.npmrc` |  | `/home/u/.npmrc` |
| `pypirc`: PyPI upload credentials | definite | `.pypirc` |  | `/home/u/.pypirc` |
| `docker-config`: Docker client configuration, which holds registry logins unless a credential helper does | likely | `.docker/config.json` |  | `/home/u/.docker/config.json` |

## Network and database client credentials (`network`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `netrc`: netrc machine logins | definite | `.netrc` |  | `/home/u/.netrc` |
| `pgpass`: PostgreSQL password file | definite | `.pgpass` |  | `/home/u/.pgpass` |

## Infrastructure state and provisioning credentials (`infrastructure`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `terraform-state`: Terraform state, which records resource secrets in clear text | likely | `*.tfstate`<br>`*.tfstate.backup` |  | `/repo/terraform.tfstate` |
| `terraform-cli-credentials`: Terraform CLI credentials | definite | `.terraform.d/credentials.tfrc.json` |  | `/home/u/.terraform.d/credentials.tfrc.json` |

## Files named for what they hold (`generic`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `credentials-file`: A file named credentials | likely | `credentials`<br>`credentials.{json,toml}` |  | `/srv/app/credentials` |
| `secrets-file`: A data file named secrets | likely | `secrets.{yaml,yml,json}` |  | `/repo/config/secrets.yaml` |

## Kubernetes credentials (`kubernetes`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `kubeconfig`: Kubernetes client configuration, which holds cluster credentials or names where they are | likely | `.kube/config`<br>`.kube/config-*`<br>`kubeconfig`<br>`kubeconfig.{yaml,yml}`<br>`.kube/*kubeconfig*` |  | `/home/u/.kube/config` |
| `kubernetes-token-file`: A file named for a Kubernetes or cluster token | likely | `{sa,kube,kubernetes,k8s,cluster,admin,user,deploy,bootstrap}{-,_,}token{,.csv}`<br>`service{-,_,}account{-,_,}token{,.csv}`<br>`.kube/**/*token*` |  | `/etc/kubernetes/admin-token` |
| `kubernetes-service-account-token`: Mounted Kubernetes service account token | definite | `var/run/secrets/kubernetes.io/serviceaccount/token` |  | `/var/run/secrets/kubernetes.io/serviceaccount/token` |

## GnuPG keyrings (`gpg`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `gpg-private-keys`: GnuPG private keys | definite | `.gnupg/private-keys-v1.d/**`<br>`secring.gpg` |  | `/home/u/.gnupg/private-keys-v1.d/0A1B2C3D.key` |
| `gpg-key-material`: GnuPG revocation certificates and exported key files | likely | `.gnupg/openpgp-revocs.d/**`<br>`.gnupg/*.key` |  | `/home/u/.gnupg/openpgp-revocs.d/0A1B2C3D.rev` |

## Password manager stores (`password-manager`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `keepass-database`: KeePass database | definite | `*.kdbx` |  | `/home/u/Documents/Passwords.kdbx` |
| `pass-store`: pass password store | definite | `.password-store/**` |  | `/home/u/.password-store/work/github.gpg` |
| `password-manager-data`: 1Password, Bitwarden and LastPass local data | likely | `{.1password,1Password}/**`<br>`.config/Bitwarden/**`<br>`{.lastpass,LastPass}/**`<br>`.bitwarden/**`<br>`.config/Bitwarden*.sqlite`<br>`.config/Bitwarden*.sqlite.*` |  | `/home/u/.1password/agent.sock` |

## Browser session stores (`browser`)

| Store | Tier | Paths | Except | Example |
|---|---|---|---|---|
| `firefox-session-store`: Firefox cookies and saved logins | definite | `cookies.sqlite`<br>`logins.json` |  | `/home/u/.mozilla/firefox/abc123.default-release/cookies.sqlite` |
| `browser-profile-session-store`: Cookies and saved logins in a browser profile | definite | `{google-chrome,chromium,chrome,brave-browser,brave browser,bravebrowser,bravesoftware,microsoft-edge,microsoft edge,microsoftedge,edge,firefox,mozilla,safari}{,-beta, beta,-dev, dev,-canary, canary,-unstable, unstable,-sxs, sxs}/**/{*cookies,*login data}` |  | `/home/u/.config/google-chrome/Default/Cookies` |

## Generated expressions

What a rule or a query uses is a selection of the catalog as one expression. These are the two
selections of every kind, over a path `p` in CEL and a column `FilePath` in KQL. The regular
expressions are RE2 syntax, which both read the same way.

### `definite` stores

CEL:

```
(p.matches("(?i)(?:^|[/\\\\])(?:\\.aws[/\\\\]credentials$|application_default_credentials\\.json$|\\.config[/\\\\]gcloud[/\\\\](?:credentials|access_tokens)\\.db$|\\.azure[/\\\\](?:msal_token_cache|accesstokens)\\.json$|\\.env$|\\.git-credentials$|\\.config[/\\\\]git[/\\\\]credentials$|\\.pypirc$|\\.netrc$|\\.pgpass$|\\.terraform\\.d[/\\\\]credentials\\.tfrc\\.json$|var[/\\\\]run[/\\\\]secrets[/\\\\]kubernetes\\.io[/\\\\]serviceaccount[/\\\\]token$|\\.gnupg[/\\\\]private-keys-v1\\.d[/\\\\]|secring\\.gpg$|[^/\\\\]*\\.kdbx$|\\.password-store[/\\\\]|cookies\\.sqlite$|logins\\.json$|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\\\](?:[^/\\\\]+[/\\\\])*(?:[^/\\\\]*cookies|[^/\\\\]*login data)$)") || (p.matches("(?i)(?:^|[/\\\\])(?:id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)$|\\.ssh[/\\\\]id_[^/\\\\]*$)") && !p.matches("(?i)(?:^|[/\\\\])(?:[^/\\\\]*\\.pub$)")))
```

KQL:

```
(FilePath matches regex @"(?i)(?:^|[/\\])(?:\.aws[/\\]credentials$|application_default_credentials\.json$|\.config[/\\]gcloud[/\\](?:credentials|access_tokens)\.db$|\.azure[/\\](?:msal_token_cache|accesstokens)\.json$|\.env$|\.git-credentials$|\.config[/\\]git[/\\]credentials$|\.pypirc$|\.netrc$|\.pgpass$|\.terraform\.d[/\\]credentials\.tfrc\.json$|var[/\\]run[/\\]secrets[/\\]kubernetes\.io[/\\]serviceaccount[/\\]token$|\.gnupg[/\\]private-keys-v1\.d[/\\]|secring\.gpg$|[^/\\]*\.kdbx$|\.password-store[/\\]|cookies\.sqlite$|logins\.json$|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\](?:[^/\\]+[/\\])*(?:[^/\\]*cookies|[^/\\]*login data)$)" or (FilePath matches regex @"(?i)(?:^|[/\\])(?:id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)$|\.ssh[/\\]id_[^/\\]*$)" and not(FilePath matches regex @"(?i)(?:^|[/\\])(?:[^/\\]*\.pub$)")))
```

### `likely` and `definite` stores

CEL:

```
(p.matches("(?i)(?:^|[/\\\\])(?:\\.aws[/\\\\]credentials$|application_default_credentials\\.json$|\\.config[/\\\\]gcloud[/\\\\](?:credentials|access_tokens)\\.db$|\\.azure[/\\\\](?:msal_token_cache|accesstokens)\\.json$|\\.env$|\\.git-credentials$|\\.config[/\\\\]git[/\\\\]credentials$|\\.config[/\\\\]gh[/\\\\]hosts\\.yml$|\\.npmrc$|\\.pypirc$|\\.docker[/\\\\]config\\.json$|\\.netrc$|\\.pgpass$|[^/\\\\]*\\.tfstate$|[^/\\\\]*\\.tfstate\\.backup$|\\.terraform\\.d[/\\\\]credentials\\.tfrc\\.json$|credentials$|credentials\\.(?:json|toml)$|secrets\\.(?:yaml|yml|json)$|\\.kube[/\\\\]config$|\\.kube[/\\\\]config-[^/\\\\]*$|kubeconfig$|kubeconfig\\.(?:yaml|yml)$|\\.kube[/\\\\][^/\\\\]*kubeconfig[^/\\\\]*$|(?:sa|kube|kubernetes|k8s|cluster|admin|user|deploy|bootstrap)(?:-|_|)token(?:|\\.csv)$|service(?:-|_|)account(?:-|_|)token(?:|\\.csv)$|\\.kube[/\\\\](?:[^/\\\\]+[/\\\\])*[^/\\\\]*token[^/\\\\]*$|var[/\\\\]run[/\\\\]secrets[/\\\\]kubernetes\\.io[/\\\\]serviceaccount[/\\\\]token$|\\.gnupg[/\\\\]private-keys-v1\\.d[/\\\\]|secring\\.gpg$|\\.gnupg[/\\\\]openpgp-revocs\\.d[/\\\\]|\\.gnupg[/\\\\][^/\\\\]*\\.key$|[^/\\\\]*\\.kdbx$|\\.password-store[/\\\\]|(?:\\.1password|1Password)[/\\\\]|\\.config[/\\\\]Bitwarden[/\\\\]|(?:\\.lastpass|LastPass)[/\\\\]|\\.bitwarden[/\\\\]|\\.config[/\\\\]Bitwarden[^/\\\\]*\\.sqlite$|\\.config[/\\\\]Bitwarden[^/\\\\]*\\.sqlite\\.[^/\\\\]*$|cookies\\.sqlite$|logins\\.json$|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\\\](?:[^/\\\\]+[/\\\\])*(?:[^/\\\\]*cookies|[^/\\\\]*login data)$)") || (p.matches("(?i)(?:^|[/\\\\])(?:id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)$|\\.ssh[/\\\\]id_[^/\\\\]*$)") && !p.matches("(?i)(?:^|[/\\\\])(?:[^/\\\\]*\\.pub$)")) || (p.matches("(?i)(?:^|[/\\\\])(?:\\.env\\.[^/\\\\]*$)") && !p.matches("(?i)(?:^|[/\\\\])(?:\\.env\\.(?:example|sample|template|dist)$|\\.env\\.[^/\\\\]*\\.(?:example|sample|template|dist)$)")))
```

KQL:

```
(FilePath matches regex @"(?i)(?:^|[/\\])(?:\.aws[/\\]credentials$|application_default_credentials\.json$|\.config[/\\]gcloud[/\\](?:credentials|access_tokens)\.db$|\.azure[/\\](?:msal_token_cache|accesstokens)\.json$|\.env$|\.git-credentials$|\.config[/\\]git[/\\]credentials$|\.config[/\\]gh[/\\]hosts\.yml$|\.npmrc$|\.pypirc$|\.docker[/\\]config\.json$|\.netrc$|\.pgpass$|[^/\\]*\.tfstate$|[^/\\]*\.tfstate\.backup$|\.terraform\.d[/\\]credentials\.tfrc\.json$|credentials$|credentials\.(?:json|toml)$|secrets\.(?:yaml|yml|json)$|\.kube[/\\]config$|\.kube[/\\]config-[^/\\]*$|kubeconfig$|kubeconfig\.(?:yaml|yml)$|\.kube[/\\][^/\\]*kubeconfig[^/\\]*$|(?:sa|kube|kubernetes|k8s|cluster|admin|user|deploy|bootstrap)(?:-|_|)token(?:|\.csv)$|service(?:-|_|)account(?:-|_|)token(?:|\.csv)$|\.kube[/\\](?:[^/\\]+[/\\])*[^/\\]*token[^/\\]*$|var[/\\]run[/\\]secrets[/\\]kubernetes\.io[/\\]serviceaccount[/\\]token$|\.gnupg[/\\]private-keys-v1\.d[/\\]|secring\.gpg$|\.gnupg[/\\]openpgp-revocs\.d[/\\]|\.gnupg[/\\][^/\\]*\.key$|[^/\\]*\.kdbx$|\.password-store[/\\]|(?:\.1password|1Password)[/\\]|\.config[/\\]Bitwarden[/\\]|(?:\.lastpass|LastPass)[/\\]|\.bitwarden[/\\]|\.config[/\\]Bitwarden[^/\\]*\.sqlite$|\.config[/\\]Bitwarden[^/\\]*\.sqlite\.[^/\\]*$|cookies\.sqlite$|logins\.json$|(?:google-chrome|chromium|chrome|brave-browser|brave browser|bravebrowser|bravesoftware|microsoft-edge|microsoft edge|microsoftedge|edge|firefox|mozilla|safari)(?:|-beta| beta|-dev| dev|-canary| canary|-unstable| unstable|-sxs| sxs)[/\\](?:[^/\\]+[/\\])*(?:[^/\\]*cookies|[^/\\]*login data)$)" or (FilePath matches regex @"(?i)(?:^|[/\\])(?:id_(?:rsa|ed25519|ecdsa|dsa|ecdsa_sk|ed25519_sk)$|\.ssh[/\\]id_[^/\\]*$)" and not(FilePath matches regex @"(?i)(?:^|[/\\])(?:[^/\\]*\.pub$)")) or (FilePath matches regex @"(?i)(?:^|[/\\])(?:\.env\.[^/\\]*$)" and not(FilePath matches regex @"(?i)(?:^|[/\\])(?:\.env\.(?:example|sample|template|dist)$|\.env\.[^/\\]*\.(?:example|sample|template|dist)$)")))
```
