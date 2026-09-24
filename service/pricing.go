// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
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

package service

// pricing.go is the one place Hanzo's price for hosted compute is defined.
//
// A running hosted machine costs Hanzo three things by the hour: the instance,
// at AWS's on-demand list price; its public IPv4 address, which AWS charges for
// by the hour while it is attached; and its root volume, gp3 storage billed by
// the GB-month. A STOPPED machine releases the instance and the address and
// keeps costing the volume. Hanzo sells each at cost plus the platform fee of one
// third, rounded UP to the whole cent per hour: the ledger debits whole cents and
// a paid product never under-charges. All arithmetic is integer, so the price a
// quote shows is the price the meter debits, to the cent.
//
// Outbound transfer to the internet is the fourth cost, by the GiB. Its price is
// fixed here, and nothing charges it yet: the one per-instance count EC2 keeps,
// NetworkOut, is every byte an instance sends, in-region traffic included, which
// is not the transfer AWS bills. It prices the transfer a machine could run up,
// which is what the sweep stops a machine for (metering.go).
const (
	// feeNum/feeDen is the multiplier over cost: 4/3, list plus one third.
	feeNum = 4
	feeDen = 3

	// microsPerCent converts micro-dollars to cents.
	microsPerCent = 10_000

	// hoursPerMonth is the month AWS prices storage in and quotes monthly figures by.
	hoursPerMonth = 730

	// gp3MicrosPerGBMonth is EBS gp3 storage in us-east-1: $0.08 per GB-month.
	gp3MicrosPerGBMonth = 80_000

	// ipv4MicrosPerHour is one public IPv4 address in use: $0.005 per hour.
	ipv4MicrosPerHour = 5_000

	// transferMicrosPerGiB is data transferred out of EC2 to the internet in
	// us-east-1: $0.09 per GiB (AWS's "GB" is 2^30 bytes).
	transferMicrosPerGiB = 90_000

	// gibBytes is the unit transfer is priced in: 2^30 bytes.
	gibBytes = 1 << 30
)

// TransferCentsPerGiB is Hanzo's price for outbound transfer, in whole cents per
// GiB: cost plus the fee, which is exactly 12 cents.
const TransferCentsPerGiB = transferMicrosPerGiB * feeNum / (feeDen * microsPerCent)

// hourlyCents is Hanzo's price in whole cents per RUNNING hour for an instance
// listed at listMicros micro-dollars per hour, with its public IPv4 address and a
// diskGB gp3 root volume. Zero when there is no instance to charge for.
func hourlyCents(listMicros, diskGB int64) int64 {
	if listMicros <= 0 {
		return 0
	}
	return monthCents((listMicros+ipv4MicrosPerHour)*hoursPerMonth + diskGB*gp3MicrosPerGBMonth)
}

// stoppedCents is Hanzo's price in whole cents per STOPPED hour: the diskGB gp3
// root volume alone.
func stoppedCents(diskGB int64) int64 {
	return monthCents(diskGB * gp3MicrosPerGBMonth)
}

// monthCents is a cost over one 730-hour month in micro-dollars — so the storage
// rate needs no division before the fee is applied — as Hanzo's price per hour
// in whole cents, rounded up.
func monthCents(monthMicros int64) int64 {
	if monthMicros <= 0 {
		return 0
	}
	num := monthMicros * feeNum
	den := int64(hoursPerMonth * feeDen * microsPerCent)
	return (num + den - 1) / den
}

// transferCents is what bytes of transfer cost at TransferCentsPerGiB, rounded
// up to the whole cent.
func transferCents(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return (bytes*TransferCentsPerGiB + gibBytes - 1) / gibBytes
}
