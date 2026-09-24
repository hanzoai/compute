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
// third, rounded UP to the whole cent: the ledger debits whole cents and a paid
// product never under-charges. All arithmetic is integer, so the price a quote
// shows is the price the meter debits, to the cent.
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
)

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
