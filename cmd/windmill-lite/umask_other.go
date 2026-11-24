//go:build !unix

package main

import "errors"

// privateUmask refuses: the plugin command keeps its sockets private with
// a umask, which only a Unix system has.
func privateUmask() (uint32, error) {
	return 0, errors.New("the plugin command needs a Unix system")
}
