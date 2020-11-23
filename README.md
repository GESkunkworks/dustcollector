# dustcollector

Package to analyze the EBS snapshots in an account and determines how much money you could save by deleting snapshots that are missing their volumes. 

Determines whether snapshots have current volumes, are registered as AMIs, used in LaunchConfigurations, etc. then provides recommendations for deletion. 

This is intended to be run from another golang project's main package.

Sample Usage
```
package main

import (
	"fmt"
	"github.com/GESkunkworks/dustcollector"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/aws"	
)

func main() {
	sess = session.New()
	dateFilter := "2019-01-01"
	expInput := dustcollector.ExpeditionInput{
		Session:    sess,
		DateFilter: &dateFilter,
	}
	exp := dustcollector.New(&expInput)
	err = exp.Start()
	if err != nil { panic(err) }
	for _, line := range(exp.GetRecommendations()) {
		fmt.Println(line)
	}
}
```

Sample Output
```
After analyzing the account we can see that there are 5 snapshots that can be deleted because they were created before 2019-01-01 and are not used in any AutoScaling group or AMI sharing capacity. However, before these snapshots can be deleted several other resources need to be deleted first. Below you can find the ordered deletion plan:


Some of the snapshots we need to delete are currently registered as AMIs or used in Launch Templates/Configs. However we've detected that those AMI's and Launch Templates/Configs are not used in any autoscaling group. This doesn't mean they're not being used by someone (e.g., referenced in a cloudformation template). You should be safe to delete them but you should always check to be sure

If you feel comfortable then here's the plan:

Delete the following LaunchTemplates first:
        test-lt
then delete the following LaunchConfigurations:
        test-snap-lc
then delete the following AMIs:
        ami-a7ce9bdd
        ami-6cee4b16
then finally delete the following Snapshots:
        snap-092ab265885243a2d
        snap-005ccdfd0fedb77b6
        snap-06e70bf98b9e43b2f
        snap-0a4795e305f1bc40d
        snap-07a4f8539c10e0dc7
3 snapshots were spared because their EBS volume still exists
1 snapshots were spared because they were associated with an autoscaling group, were shared directly to another account, or were registered as an AMI that was shared to another account.
Total size of eligible for deletion is 40 GB. At a per GB-month rate of $0.050000 there is a potential savings of $2.000000
```

Read full [godocs here](https://godoc.org/github.com/GESkunkworks/dustcollector)

