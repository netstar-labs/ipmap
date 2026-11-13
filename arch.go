package ipmap

import "math"

// ipmap requires a 64-bit platform, and says so here, at compile time, instead
// of leaving a 32-bit build to fail somewhere else with an unrelated message
// (it used to be builder.go's "math.MaxUint32 overflows int"). Open takes
// section lengths, offsets and counts from an untrusted file as 64-bit values
// and must reason about them without truncation; with a 32-bit int that is a
// silent-truncation and negative-id hazard in the one function whose promise is
// that it never panics, reads out of bounds, or answers wrongly. The use case
// is also mmapping artifacts larger than RAM, which a 32-bit address space
// cannot do.
//
// The declaration is 1 on a 64-bit int and 0 on a 32-bit one; subtracting 1
// from it into a uint is then a constant overflow exactly when int is 32 bits,
// and the compiler reports this line. The file is named to sort ahead of
// builder.go, whose own overflow was the first error a 32-bit build used to
// report, so this is now the first line the reader sees.
const ipmapRequires64BitInt = math.MaxInt >> 62

const _ uint = ipmapRequires64BitInt - 1
