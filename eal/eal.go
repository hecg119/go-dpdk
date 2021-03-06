package eal

/*
#include <rte_config.h>
#include <rte_eal.h>
#include <rte_lcore.h>
*/
import "C"

// StopLcores sends signal to EAL threads to finish execution of
// go-dpdk lcore function executor.
//
// Warning: it will block until all lcore threads finish execution.
func StopLcores() {
	lcores := Lcores()
	ch := make(chan error, len(lcores))

	for _, id := range lcores {
		ExecOnLcoreAsync(id, ch, func(ctx *LcoreCtx) {
			ctx.done = true
		})
	}

	for range lcores {
		<-ch
	}
}

// ExecOnLcoreAsync sends fn to execute on CPU logical core lcoreID,
// i.e. in EAL-owned thread on that lcore.
//
// Possible panic in lcore function will be intercepted and returned
// as an error of type ErrLcorePanic through ret channel specified by
// caller. If lcoreID is invalid, ErrLcoreInvalid error will be
// returned the same way.
//
// The function returns ret. You may specify ret to be nil, in which
// case no error will be reported.
func ExecOnLcoreAsync(lcoreID uint, ret chan error, fn func(*LcoreCtx)) <-chan error {
	if ctx, ok := goEAL.lcores[lcoreID]; ok {
		ctx.ch <- &lcoreJob{fn, ret}
	} else if ret != nil {
		ret <- ErrLcoreInvalid
	}
	return ret
}

// ExecOnLcore sends fn to execute on CPU logical core lcoreID, i.e.
// in EAL-owned thread on that lcore. Then it waits for the execution
// to finish and returns the execution result.
//
// Possible panic in lcore function will be intercepted and returned
// as an error of type ErrLcorePanic. If lcoreID is invalid,
// ErrLcoreInvalid error will be returned.
func ExecOnLcore(lcoreID uint, fn func(*LcoreCtx)) error {
	return <-ExecOnLcoreAsync(lcoreID, make(chan error, 1), fn)
}

// ExecOnMasterAsync is a shortcut for ExecOnLcoreAsync with master
// lcore as a destination.
func ExecOnMasterAsync(ret chan error, fn func(*LcoreCtx)) <-chan error {
	return ExecOnLcoreAsync(GetMasterLcore(), ret, fn)
}

// ExecOnMaster is a shortcut for ExecOnLcore with master lcore as a
// destination.
func ExecOnMaster(fn func(*LcoreCtx)) error {
	return ExecOnLcore(GetMasterLcore(), fn)
}

type lcoresIter struct {
	i  C.uint
	sm C.int
}

func (iter *lcoresIter) next() bool {
	iter.i = C.rte_get_next_lcore(iter.i, iter.sm, 0)
	return iter.i < C.RTE_MAX_LCORE
}

// If skipMaster is 0, master lcore will be included in the result.
// Otherwise, it will miss the output.
func getLcores(skipMaster int) (out []uint) {
	c := &lcoresIter{i: ^C.uint(0), sm: C.int(skipMaster)}
	for c.next() {
		out = append(out, uint(c.i))
	}
	return out
}

// Lcores returns all lcores registered in EAL.
func Lcores() []uint {
	return getLcores(0)
}

// LcoresSlave returns all slave lcores registered in EAL.
// Lcore is slave if it is not master.
func LcoresSlave() []uint {
	return getLcores(1)
}

// HasHugePages tells if huge pages are activated.
func HasHugePages() bool {
	return int(C.rte_eal_has_hugepages()) != 0
}

// HasPCI tells whether EAL is using PCI bus. Disabled by –no-pci
// option.
func HasPCI() bool {
	return int(C.rte_eal_has_pci()) != 0
}

// ProcessType returns the current process type.
func ProcessType() int {
	return int(C.rte_eal_process_type())
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
