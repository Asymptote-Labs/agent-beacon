// Package accounthome resolves the home directory of the account this process runs as, from the
// account itself rather than $HOME or USERPROFILE. The environment can come from somewhere else: a
// repository can supply a hook's environment, and a process started under other credentials can
// inherit its parent's. Use it where a path under the home decides what Beacon trusts or where an
// account's own files go; it cannot be redirected by changing the environment.
package accounthome

// Dir is the home directory of the account this process runs as. Tests replace it.
var Dir = lookup
