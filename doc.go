// Package dustcollector seeks to save you money on your AWS bill by
// analyzing an AWS account for old, orphaned EBS snapshots that are
// not being used by any AutoScaling Groups, Launch Templates, Launch
// Configurations, or AMIs that are shared outside the account.
//
// In older AWS accounts with a lot of EBS activity this can be a
// non-trivial amount of money each month. 
// 
//   Note: This is likened to a gold miner collecting gold dust to make
//   nuggets and bars from gold dust hence the struct names.
// 
// Snapshot Cost Overview
//
// EBS snapshots are billed based on GB-month rate. So if you have a 500GB 
// volume with a single snapshot and the rate is $0.05 per GB-month then 
// that snapshot will cost $25/month to store. Snapshots after the initial 
// snapshot are only the difference of what's changed since the last snapshot. 
// So if you snapshot the volume again and you changed 50GB worth of data 
// then you are billed for 550 GB-month of data so your bill would 
// increase to $27.50 the next month. 
//
// If an EBS volume is deleted then the snapshot will remain in case you 
// want to restore the volume at some point in the future. However, most of 
// the time people just simply forget to delete snapshots when they terminate 
// infrastructure or they intend to keep the snapshot for only a few months 
// but then forget about it. This can leave snapshots laying around for 
// years and the costs can add up. 
// 
// This tool is designed to help find that snapshot volume so you can 
// clean it up and save some money. 
//
// Usage
// 
// Create a dustcollector.Expedition and call the Start() method on it. 
// After the expedition is complete you can collect a summary and 
// deletion plan by calling GetRecommendations()
//
// It provides methods to export the raw Snapshot information to CSV
// that contains the additional metadata that was collected such as 
// AMI's registered with the snapshots, LaunchConfigurations, and 
// AutoScaling groups tied back to the snapshot. This data format
// is referred to as a Nugget.
//
// It provides methods to export another format of the raw Snapshot
// data aggregated by common volume ID to CSV. This is useful for calculating
// potential cost savings. This data format is referred to as a Bar.
//
// Sample
//
// Below is a sample main package you could use to start a dustcollector
// Expedition and collect results.
//
//   package main
//   
//   import (
//   	"fmt"
//   	"github.com/GESkunkworks/dustcollector"
//   	"github.com/aws/aws-sdk-go/aws/session"
//   	"github.com/aws/aws-sdk-go/aws"	
//   )
//   
//   func main() {
//   	sess = session.New()
//   	dateFilter := "2019-01-01"
//   	expInput := dustcollector.ExpeditionInput{
//   		Session:    sess,
//   		DateFilter: &dateFilter,
//   	}
//   	exp := dustcollector.New(&expInput)
//   	err = exp.Start()
//   	if err != nil { panic(err) }
//   	for _, line := range(exp.GetRecommendations()) {
//   		fmt.Println(line)
//   	}
//   }
package dustcollector

