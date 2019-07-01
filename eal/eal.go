/*
Package eal wraps EAL initialization and provides some additional functionality
on top of that. Every CPU's logical core which is setup by EAL runs its own
function which essentially receives functions to execute via Go channel. So you
may run arbitrary Go code in the context of EAL thread.

EAL may be initialized via command line string, parsed command line string or a
set of Options.

Please note that some functions may be called only in EAL thread because of TLS
(Thread Local Storage) dependency.

API is a subject to change. Be aware.
*/
package eal

/*
#include <stdlib.h>

#include <rte_config.h>
#include <rte_eal.h>
#include <rte_errno.h>
#include <rte_lcore.h>

extern int lcoreFuncListener(void *arg);
*/
import "C"

import (
	"log"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"github.com/yerden/go-dpdk/common"
)

// Maximum number of lcores configured during DPDK compile-time.
const (
	MaxLcore = C.RTE_MAX_LCORE
)

// The type of process in a linux, multi-process setup.
const (
	ProcAuto      = C.RTE_PROC_AUTO
	ProcPrimary   = C.RTE_PROC_PRIMARY
	ProcSecondary = C.RTE_PROC_SECONDARY
)

// LcoreFunc is the function prototype to be executed by EAL-owned
// threads.
type LcoreFunc func(*Lcore)

// Lcore is a per-lcore context and is supplied to LcoreFunc as an
// argument.
type Lcore struct {
	// Value is a user-specified context. You may change it as you
	// will and it will persist across LcoreFunc invocations.
	Value interface{}
	// Id is a this thread's CPU logical core id.
	ID uint
	// SocketID is a this thread's CPU socket id.
	SocketID uint

	// channel to receive LcoreFunc to execute.
	ch chan LcoreFunc
}

type ealConfig struct {
	lcores [MaxLcore]*Lcore
}

var (
	// goEAL is the storage for all EAL lcore threads configuration.
	goEAL = &ealConfig{}
)

func panicCatcher(fn LcoreFunc, lc *Lcore) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		// Report the lcore ID and the panic error
		log.Printf("panic on lcore %d: %v", lc.ID, r)

		// this function is called from runtime package, so to
		// unwind the stack we may skip (1) runtime.Callers
		// function, (2) this caller function and whatever is left
		// of runtime package.
		pc := make([]uintptr, 10)
		n := runtime.Callers(2, pc)
		frames := runtime.CallersFrames(pc[:n])
		for {
			frame, more := frames.Next()
			if !more {
				break
			}
			if strings.HasPrefix(frame.Function, "runtime.") {
				continue
			}
			log.Printf("... at %s:%d, %s\n", frame.File, frame.Line,
				frame.Function)
		}
	}()
	fn(lc)
}

// to run as lcore_function_t
//export lcoreFuncListener
func lcoreFuncListener(unsafe.Pointer) C.int {
	lc := goEAL.lcores[LcoreID()]
	log.Printf("lcore %d started", LcoreID())
	defer log.Printf("lcore %d exited", LcoreID())

	for fn := range lc.ch {
		lc.refresh()
		if fn == nil {
			break
		}
		panicCatcher(fn, lc)
	}
	return 0
}

func (lc *Lcore) refresh() {
	lc.ID = LcoreID()
	lc.SocketID = uint(C.rte_lcore_to_socket_id(C.uint(lc.ID)))
}

// ExecuteOnLcore sends fn to execute on CPU logical core lcoreID, i.e
// in EAL-owned thread on that lcore. If lcoreID references unknown
// lcore (i.e. not registered by EAL) the function does nothing.
func ExecuteOnLcore(lcoreID uint, fn LcoreFunc) {
	if lc := goEAL.lcores[lcoreID]; lc != nil {
		lc.ch <- fn
	}
}

// ExecuteOnMaster is a shortcut for ExecuteOnLcore with master lcore
// as a destination.
func ExecuteOnMaster(fn LcoreFunc) {
	ExecuteOnLcore(GetMasterLcore(), fn)
}

// ForeachLcore iterates through all CPU logical cores initialized by
// EAL. If skipMaster is true the iteration will skip master lcore.
func ForeachLcore(skipMaster bool, f func(lcoreID uint)) {
	i := ^C.uint(0)
	sm := C.int(0)

	if skipMaster {
		sm = 1
	}

	for {
		i = C.rte_get_next_lcore(i, sm, 0)
		if i >= C.RTE_MAX_LCORE {
			break
		}
		f(uint(i))
	}
}

// call rte_eal_init and launch lcoreFuncListener on all slave lcores
// should be run in master lcore thread only
func ealInitAndLaunch(argv []string) error {
	argv = append([]string{os.Args[0]}, argv...)
	argc := C.int(len(argv))
	cArgv := make([]*C.char, argc+1) // last elem is NULL
	for i, arg := range argv {
		cArgv[i] = C.CString(arg)
	}

	defer func() {
		for _, arg := range cArgv {
			C.free(unsafe.Pointer(arg))
		}
	}()

	// initialize EAL
	if C.rte_eal_init(argc, (**C.char)(&cArgv[0])) < 0 {
		return common.Errno(nil)
	}

	// init per-lcore contexts
	ForeachLcore(false, func(lcoreID uint) {
		goEAL.lcores[lcoreID] = &Lcore{ch: make(chan LcoreFunc, 1)}
	})

	// lcore function
	fn := (*C.lcore_function_t)(C.lcoreFuncListener)

	// launch every EAL thread lcore function
	// it should be success since we've just called rte_eal_init()
	return common.Errno(C.rte_eal_mp_remote_launch(fn, nil, C.SKIP_MASTER))
}

// InitWithArgs initializes EAL as in rte_eal_init. Options are
// specified in a parsed command line string.
//
// This function initialized EAL and waits for executable functions on
// each of EAL-owned threads.
func InitWithArgs(argv []string) error {
	ch := make(chan error, 1)
	go func() {
		// we should initialize EAL and run EAL threads in a separate
		// goroutine because its thread is going to be acquired by EAL
		// and become master lcore thread
		runtime.LockOSThread()

		// initialize EAL and launch lcoreFuncListener on all slave
		// lcores, then report
		ch <- ealInitAndLaunch(argv)

		// run on master lcore
		lcoreFuncListener(nil)
	}()

	return <-ch
}

// Init initializes EAL as in rte_eal_init. Options are
// specified in a unparsed command line string. This string is parsed
// and InitWithArgs is then called upon.
func Init(argv string) error {
	return InitWithArgs(strings.Split(argv, " "))
}

// InitWithOpts initializes EAL as in rte_eal_init. Options are
// specified in array of Option-s. These options are then used to
// construct argv array and InitWithArgs is then called upon.
func InitWithOpts(opts ...Option) error {
	return InitWithArgs(OptArgs(opts))
}

// HasHugePages tells if huge pages are activated.
func HasHugePages() bool {
	return int(C.rte_eal_has_hugepages()) != 0
}

// ProcessType returns the current process type.
func ProcessType() int {
	return int(C.rte_eal_process_type())
}

// LcoreID returns CPU logical core id. This function must be called
// only in EAL thread.
func LcoreID() uint {
	return uint(C.rte_lcore_id())
}

// SocketID returns NUMA socket where the current thread resides. This
// function must be called only in EAL thread.
func SocketID() uint {
	return uint(C.rte_socket_id())
}

// LcoreCount returns number of CPU logical cores configured by EAL.
func LcoreCount() uint {
	return uint(C.rte_lcore_count())
}

// GetMasterLcore returns CPU logical core id where the master thread
// is executed.
func GetMasterLcore() uint {
	return uint(C.rte_get_master_lcore())
}
