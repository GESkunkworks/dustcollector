package dustcollector

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/inconshreveable/log15"
)

// describeLaunchTemplates describes all launch templates for the given session
// including pagination handling. It returns a slice of LaunchTemplateVersion pointers
// for easy processing in other functions as well as any errors.
func (exp *Expedition) describeLaunchTemplates() (lts []*ec2.LaunchTemplateVersion, err error) {
	exp.log.Info("grabbing all latest launch template versions")
	svc := ec2.New(exp.session)
	ltVersionLatest := "$Latest"
	var versions []*string
	versions = append(versions, &ltVersionLatest)
	input := ec2.DescribeLaunchTemplateVersionsInput{
		Versions: versions,
	}
	results, err := svc.DescribeLaunchTemplateVersions(&input)
	if err != nil {
		return lts, err
	}
	lts = results.LaunchTemplateVersions
	i := 2
	max := 50
	for i < max {
		exp.log.Debug("handling launchtemplate results", "page", i)
		if results.NextToken != nil {
			input = ec2.DescribeLaunchTemplateVersionsInput{
				NextToken: results.NextToken,
			}
			results, err = svc.DescribeLaunchTemplateVersions(&input)
			if err != nil {
				return lts, err
			}
			lts = append(lts, results.LaunchTemplateVersions...)
		} else {
			break
		}
		i += 1
	}
	return lts, err
}

// describeLaunchConfigurations describes all launch configurations for the given session
// including pagination handling. It returns a slice of LaunchConfiguration pointers
// for easy processing in other functions as well as any errors.
func (exp *Expedition) describeLaunchConfigurations() (lcs []*autoscaling.LaunchConfiguration, err error) {
	exp.log.Debug("grabbing all launch configurations")
	svc := autoscaling.New(exp.session)
	input := autoscaling.DescribeLaunchConfigurationsInput{}
	results, err := svc.DescribeLaunchConfigurations(&input)
	if err != nil {
		return lcs, err
	}
	lcs = results.LaunchConfigurations
	i := 2
	max := 50
	for i < max {
		exp.log.Debug("handling launchconfig results", "page", i)
		if results.NextToken != nil {
			input = autoscaling.DescribeLaunchConfigurationsInput{
				NextToken: results.NextToken,
			}
			results, err = svc.DescribeLaunchConfigurations(&input)
			if err != nil {
				return lcs, err
			}
			lcs = append(lcs, results.LaunchConfigurations...)
		} else {
			break
		}
		i += 1
	}
	return lcs, err
}

// imageSharedTo takes a session and an AMI ID string and looks up the sharing
// properties of that AMI. It returns a slice of account number strings where
// the image is shared and any error. If the image is "public" it returns a slice of length
// 1 with the only item being "all".
func (exp *Expedition) imageSharedTo(ami string) (accts []string, err error) {
	exp.log.Debug("describing image attributes for sharing", "ami", ami)
	svc := ec2.New(exp.session)
	launchPermissionAttr := "launchPermission"
	input := ec2.DescribeImageAttributeInput{
		Attribute: &launchPermissionAttr,
		ImageId:   &ami,
	}
	results, err := svc.DescribeImageAttribute(&input)
	if err != nil {
		return accts, err
	}
	for _, perm := range results.LaunchPermissions {
		if perm.Group != nil {
			// catch case where image shared public as string "all" I think
			accts = append(accts, *perm.Group)
		}
		if perm.UserId != nil {
			// grab acct nums
			accts = append(accts, *perm.UserId)
		}
	}
	return accts, err

}

// populateNuggets takes a session and a slice of Nugget (snapshot with additional
// metadata) and runs several checks on them to understand where the snapshots
// are being used for other AWS services including: LaunchTemplates, LaunchConfigurations,
// AMIs, CreateVolumePermissions, and AutoScalingGroups. It adds this additional
// metadata to each nugget object and returns any errors.
func (exp *Expedition) populateNuggets() (err error) {

	// grab all launch configs for later lookup
	lcs, err := exp.describeLaunchConfigurations()
	if err != nil {
		return err
	}
	// grab all launch templates for later lookup
	lts, err := exp.describeLaunchTemplates()
	if err != nil {
		return err
	}

	images, err := describeImagesOwnedByThisAccount(exp.session)
	if err != nil {
		return err
	}
	// grab autoscaling groups so we can find launchconfigs in use
	asgs, err := describeASGs(exp.session)
	if err != nil {
		return err
	}
	// loop through images result and find out if there is an orphaned
	// snapshot with same ID
	for _, image := range images {
		for _, snap := range exp.Nuggets {
			for _, bdm := range image.BlockDeviceMappings {
				if bdm.Ebs != nil {
					if bdm.Ebs.SnapshotId != nil {
						if *snap.Snap.SnapshotId == *bdm.Ebs.SnapshotId {
							msg := fmt.Sprintf(
								"mapping %s to %s", *snap.Snap.SnapshotId, *image.ImageId,
							)
							exp.log.Debug(msg)
							snap.AMIIDs = append(snap.AMIIDs, *image.ImageId)
							// now find out if any launch configs use this AMI or snapshot ID
							snap.LCs = lcsWithSnapImage(lcs, *snap.Snap.SnapshotId, *image.ImageId)
							// now find out if any launch templates use this AMI or snapshot ID
							snap.LTs = ltsWithSnapImage(lts, *snap.Snap.SnapshotId, *image.ImageId)
							// now find out if any AGS use this launch template/config
							for _, lc := range snap.LCs {
								_, snap.ASGs = lcInASGs(lc, asgs)
							}
							for _, lt := range snap.LTs {
								_, snap.ASGs = ltInASGs(lt, asgs)
							}
							// now find out where image is shared to
							snap.AMISharedWith, err = exp.imageSharedTo(*image.ImageId)
							if err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}
	return err
}

func (exp *Expedition) addBars() {
	for _, nug := range exp.Nuggets {
		exp.addBar(nug)
	}
}

func (exp *Expedition) addBar(n *Nugget) {
	found := false
	// group snapshots by volumeID
	for _, bar := range exp.Bars {
		if *bar.VolumeId == *n.Snap.VolumeId {
			n.parentBar = bar
			bar.Nuggets = append(bar.Nuggets, n)
			found = true
		}
	}
	if !found {
		// make new bar
		b := Bar{
			VolumeId: n.Snap.VolumeId,
		}
		n.parentBar = &b
		b.Nuggets = append(b.Nuggets, n)
		b.HasVol = n.HasVol
		exp.Bars = append(exp.Bars, &b)
	}
}

// GetRecommendations takes all of the information acquired during the
// Expedition and returns a string slice containing recommendations
// for an ordered action plan for removing orphaned snapshots
func (exp *Expedition) GetRecommendations() (msg []string) {
	return exp.recommendations
}

// setRecommendations takes all of the information acquired during the
// Expedition and sets a string slice containing recommendations
// for an ordered action plan for removing orphaned resources
func (exp *Expedition) setRecommendations() {
	var msg []string
	var countAsgShare int
	var countHasVol int
	var barsToDelete []*Bar
	for _, bar := range exp.Bars {
		if !bar.HasVol {
			for _, nug := range bar.Nuggets {
				if len(nug.ASGs) == 0 && len(nug.AMISharedWith) == 0 {
					// safe to delete
					exp.LtsToDelete = append(exp.LtsToDelete, nug.LTs...)
					exp.LcsToDelete = append(exp.LcsToDelete, nug.LCs...)
					exp.AmiToDelete = append(exp.AmiToDelete, nug.AMIIDs...)
					exp.SnapToDelete = append(exp.SnapToDelete, *nug.Snap.SnapshotId)
					barsToDelete = append(barsToDelete, nug.parentBar)
				} else {
					countAsgShare++
				}
			}
		} else {
			countHasVol++
		}
	}
	exp.LtsToDelete = dedupeString(exp.LtsToDelete)
	exp.LcsToDelete = dedupeString(exp.LcsToDelete)
	exp.AmiToDelete = dedupeString(exp.AmiToDelete)
	exp.SnapToDelete = dedupeString(exp.SnapToDelete)
	intro := fmt.Sprintf("After analyzing the account we can see that there "+
		"are %d snapshots that can be deleted because they were created "+
		"before %s and are not used in any AutoScaling group or AMI sharing "+
		"capacity. However, before these snapshots can be deleted several "+
		"other resources need to be deleted first. Below you can find the "+
		"ordered deletion plan:\n\n", len(exp.SnapToDelete), exp.dateFilter)
	msg = append(msg, intro)
	msg = append(msg, "Some of the snapshots we need to delete are "+
		"currently registered as AMIs or used in Launch Templates/Configs. "+
		"However we've detected that those AMI's and Launch Templates/Configs "+
		"are not used in any autoscaling group. This doesn't mean they're "+
		"not being used by someone (e.g., referenced in a cloudformation "+
		"template). You should be safe to delete them but you should always "+
		"check to be sure\n\nIf you feel comfortable then here's the plan:\n")
	msg = append(msg, "Delete the following LaunchTemplates first:")
	for _, lt := range exp.LtsToDelete {
		msg = append(msg, "\t"+lt)
	}
	msg = append(msg, "then delete the following LaunchConfigurations:")
	for _, lc := range exp.LcsToDelete {
		msg = append(msg, "\t"+lc)
	}
	msg = append(msg, "then delete the following AMIs:")
	for _, ami := range exp.AmiToDelete {
		msg = append(msg, "\t"+ami)
	}
	msg = append(msg, "then finally delete the following Snapshots:")
	for _, snap := range exp.SnapToDelete {
		msg = append(msg, "\t"+snap)
	}
	msg = append(
		msg,
		fmt.Sprintf(
			"%d snapshots were spared because their EBS volume still exists",
			countHasVol,
		),
	)
	msg = append(
		msg,
		fmt.Sprintf(
			"%d snapshots were spared because they were associated with an "+
				"autoscaling group, were shared directly to another account, "+
				"or were registered as an AMI that was shared to another account.",
			countAsgShare,
		),
	)
	// now add cost analysis
	var totalGbs int64
	for _, b := range barsToDelete {
		if !b.HasVol {
			totalGbs += *b.Nuggets[0].Snap.VolumeSize
		}
	}
	totalSavings := float64(totalGbs) * exp.ebsSnapRate
	s := fmt.Sprintf(
		"Total size of eligible for deletion "+
			"is %d GB. At a per GB-month rate of $%f "+
			"there is a potential savings of $%f",
		totalGbs, exp.ebsSnapRate, totalSavings)
	msg = append(msg, s)
	exp.recommendations = msg
}

// An Expedition contains the properties and methods necessary
// to analyze the snapshots in an AWS account to determine
// which ones can be deleted. Create an ExpeditionInput object
// and pass it to this package's New method to get a new Expedition.
// From there call the Start method of the Expedition.
// When that is complete the findings can be exported using other
// methods.
type Expedition struct {
	// Bars are snapshots that are aggregated by volume ID
	// so that their estimated cost savings can be calculated.
	// This property is exported so that it could be marshalled
	// to another format if the ExportBars CSV format is not ideal
	Bars []*Bar

	// Nuggets are snapshots that have additional metadata
	// (see Nugget type). This property is exported so that
	// it could be marshalled to another format if the ExportNuggets
	// CSV format is not ideal
	Nuggets []*Nugget

	// After the Start method is complete LtsToDelete will
	// contain a list of LaunchTemplates that can be deleted because
	// they are blocking snapshot deletion but not being used in any
	// autoscaling group
	LtsToDelete []string

	// After the Start method is complete LcsToDelete will
	// contain a list of LaunchConfigurations that can be deleted because
	// they are blocking snapshot deletion but not being used in any
	// autoscaling group
	LcsToDelete []string

	// After the Start method is complete AmiToDelete will
	// contain a list of AMIs that can be deleted because
	// they are blocking snapshot deletion but not being used in any
	// autoscaling group or being shared to any other account
	AmiToDelete []string

	// After the Start method is complete SnapToDelete will
	// contain a list of Snapshots that can be deleted because
	// they have no active EBS volume, are older than provided
	// date filter, and aren't used in any autoscaling group or being
	// shared to any other account. May need to delete all blocking
	// resources in AmiToDelete, LcToDelete, LtToDelete first.
	SnapToDelete []string

	account                string
	cutoffDate             time.Time
	maxPages               int
	pageSize               int
	volBatchSize           int
	session                *session.Session
	wgq                    sync.WaitGroup
	wgv                    sync.WaitGroup
	queue                  chan []*ec2.Snapshot
	realVols               []*ec2.Volume
	log                    log15.Logger
	ebsSnapRate            float64
	dateFilter             string
	outfileRecommendations string
	outfileNuggets         string
	outfileBars            string
	recommendations        []string
}

// ExportRecommendations takes the current deletion plan and writes it to
// outfile.
func (exp *Expedition) ExportRecommendations() (err error) {
	file, err := os.Create(exp.outfileRecommendations)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, line := range exp.GetRecommendations() {
		_, err = file.WriteString(line + "\n")
		if err != nil {
			return err
		}
	}
	exp.log.Info("wrote summary to file", "filename", exp.outfileRecommendations)
	return err
}

// ExportBars takes all of the Bars associated with the current
// Expedition (volumes with additional metadata including associated
// snapshots) and writes them to outfile as csv.
func (exp *Expedition) ExportBars() (err error) {
	csvfile, err := os.Create(exp.outfileBars)
	if err != nil {
		return err
	}
	csvwriter := csv.NewWriter(csvfile)
	header := []string{"OwnerId", "SnapshotIds", "HasVolume", "StartTime", "VolumeSize"}
	csvwriter.Write(header)
	for _, bar := range exp.Bars {
		row := bar.dumpString()
		csvwriter.Write(row)
	}
	csvwriter.Flush()
	csvfile.Close()
	exp.log.Info("wrote bars to file", "filename", exp.outfileBars)
	return err
}

// ExportNuggets takes all of the current nuggets associated with current
// Expedition (snapshots with additional metadata) and writes them to a
// csv of the filename that's set upon Expedition creation.
func (exp *Expedition) ExportNuggets() (err error) {
	csvfile, err := os.Create(exp.outfileNuggets)
	if err != nil {
		return err
	}
	csvwriter := csv.NewWriter(csvfile)
	header := []string{
		"OwnerId", "SnapshotId", "ImageIds", "LaunchConfigNames",
		"LaunchTemplateNames-LATEST_VERSION_ONLY!",
		"ASGNames", "AMISharedWith", "HasVolume", "StartTime",
		"Tags", "VolumeSize", "Description"}
	csvwriter.Write(header)
	for _, nug := range exp.Nuggets {
		row := nug.dumpString()
		csvwriter.Write(row)
	}
	csvwriter.Flush()
	csvfile.Close()
	exp.log.Info("wrote nuggets to file", "filename", exp.outfileNuggets)
	return err
}

// Bar provides a means to lump snapshots together with a common volume
// in order to determine costs associated with a set of snapshots
type Bar struct {
	VolumeId *string
	Nuggets  []*Nugget
	HasVol   bool
}

func (b *Bar) dumpString() (s []string) {
	// join snapshot IDs together
	var sids []string
	for _, n := range b.Nuggets {
		sids = append(sids, *n.Snap.SnapshotId)
	}
	var ssid string
	if len(sids) > 1 {
		ssid = strings.Join(sids, "|")
	} else {
		ssid = sids[0]
	}
	s = []string{
		*b.Nuggets[0].Snap.OwnerId,
		ssid,
		strconv.FormatBool(b.HasVol),
		b.Nuggets[0].Snap.StartTime.Format("2006-01-02"),
		strconv.FormatInt(*b.Nuggets[0].Snap.VolumeSize, 10),
	}
	return s
}

// Nugget is intended to hold additional metadata about a snapshot such as:
//   * whether or not it's original volume still exists
//   * if the snapshot has any create volume permissions in other accounts
//   * if the snapshot is registered as an AMI
//     * if given AMI is shared to other accounts
//     * if given AMI is used in any Launch Configurations/Templates
//       * if given LC/LT is used in any AutoScaling Groups
//   * if the snapshot is used as a block device mapping in any launch config/template
//       * if given LC/LT is used in any AutoScaling Groups
type Nugget struct {
	// original snapshot object
	Snap *ec2.Snapshot

	// whether or not original volume exists
	HasVol bool

	// AMI ID's associated with snapshot
	AMIIDs []string

	// Account Numbers to which associated AMI ID's are shared
	AMISharedWith []string

	// Launch Configurations associated with AMI or Snapshot
	LCs []string

	// Launch Templates associated with AMI or Snapshot
	LTs []string

	// AutoScaling Groups associated with Launch Configs/Templates
	ASGs []string

	parentBar *Bar
}

// dumpString is a method to export the Nugget object as a CSV string
func (nug *Nugget) dumpString() (s []string) {
	// convert tags to string
	var tags []string
	for _, tag := range nug.Snap.Tags {
		stag := fmt.Sprintf("%s=%s", *tag.Key, *tag.Value)
		tags = append(tags, stag)
	}
	stags := strings.Join(tags, "|")
	imageIds := strings.Join(dedupeString(nug.AMIIDs), "|")
	lcs := strings.Join(dedupeString(nug.LCs), "|")
	lts := strings.Join(dedupeString(nug.LTs), "|")
	asgs := strings.Join(dedupeString(nug.ASGs), "|")
	shared := strings.Join(dedupeString(nug.AMISharedWith), "|")
	s = []string{
		*nug.Snap.OwnerId,
		*nug.Snap.SnapshotId,
		imageIds,
		lcs,
		lts,
		asgs,
		shared,
		strconv.FormatBool(nug.HasVol),
		nug.Snap.StartTime.Format("2006-01-02"),
		stags,
		strconv.FormatInt(*nug.Snap.VolumeSize, 10),
		*nug.Snap.Description,
	}
	return s
}

func (exp *Expedition) getAccountNumber() (err error) {
	exp.log.Debug("getting account number")
	svcSts := sts.New(exp.session)
	gcii := sts.GetCallerIdentityInput{}
	gci, err := svcSts.GetCallerIdentity(&gcii)
	if err != nil {
		return err
	}
	exp.account = *gci.Account
	return err
}

func (exp *Expedition) setDateFilter(datestring string) (err error) {
	// parse date filter from flags
	layout := "2006-01-02"
	exp.cutoffDate, err = time.Parse(layout, datestring)
	return err
}

func (exp *Expedition) getSnapshots() (err error) {
	var accounts []*string
	accounts = append(accounts, &exp.account)
	svc := ec2.New(exp.session)
	maxResults := int64(exp.pageSize)
	// get all snapshots
	dsi := ec2.DescribeSnapshotsInput{
		OwnerIds:   accounts,
		MaxResults: &maxResults,
	}
	exp.queue = make(chan []*ec2.Snapshot, 10)
	pageNum := 0
	totalSnaps := 0
	err = svc.DescribeSnapshotsPages(&dsi,
		func(page *ec2.DescribeSnapshotsOutput, lastPage bool) bool {
			pageNum++
			exp.log.Debug("processing page..", "page", pageNum)
			var filteredSnapshots []*ec2.Snapshot
			for _, snap := range page.Snapshots {
				exp.log.Debug(
					"checking date on snapshot",
					"date", snap.StartTime.Format("2006-01-02"),
				)
				if snap.StartTime.Before(exp.cutoffDate) {
					exp.log.Debug(
						"snapshot meets filter criteria", "cutoffdate",
						exp.cutoffDate.Format("2006-01-02"),
					)
					filteredSnapshots = append(filteredSnapshots, snap)
				}
			}
			totalSnaps += len(page.Snapshots)
			exp.log.Info(
				"Filtered snapshots page by date", "pre-filter",
				len(page.Snapshots), "post-filter", len(filteredSnapshots),
				"pageNum", pageNum,
			)
			if len(filteredSnapshots) > 0 {
				exp.queue <- filteredSnapshots
				exp.wgq.Add(1)
				go exp.buildNuggets()
			}
			return pageNum <= exp.maxPages
		})
	if err != nil {
		return err
	}
	go exp.monitorQueue()
	exp.log.Debug("collecting results from channel")
	exp.wgq.Wait() // wait for queue to finish
	exp.log.Info("Total snapshots post date filter", "snapshots_in_scope", len(exp.Nuggets))
	exp.log.Info("Total snapshots analyzed", "total-analyzed", totalSnaps)
	return err
}

func (exp *Expedition) monitorQueue() {
	exp.wgq.Wait()
	close(exp.queue)
	exp.log.Debug("closed queue channel")
}

func (exp *Expedition) describeVolumes(inVols []*string) {
	defer exp.wgv.Done()
	svc := ec2.New(exp.session)
	for _, vol := range inVols {
		dvi := ec2.DescribeVolumesInput{
			VolumeIds: []*string{vol},
		}
		// we don't care about "volume not found" errors
		// we just care about what does come back
		r, _ := svc.DescribeVolumes(&dvi)
		// we have to do each invidual volume in its own
		// describe because AWS will fail the bulk
		// request if even one volume is missing
		for _, vol := range r.Volumes {
			exp.realVols = append(exp.realVols, vol)
		}
	}
}

//func (exp *Expedition) buildNuggets(snaps []*ec2.Snapshot) {
func (exp *Expedition) buildNuggets() {
	defer exp.wgq.Done()
	exp.log.Debug("inside a buildNuggets gofunc")
	var nuggets []*Nugget
	var inVols []*string
	for _, snap := range <-exp.queue {
		inVols = append(inVols, snap.VolumeId)
		// make a new nugget
		n := Nugget{
			Snap: snap,
		}
		nuggets = append(nuggets, &n)
	}
	exp.log.Debug("build page of nuggets", "nuggets", len(nuggets))
	inVolsD := dedupeStringPointer(inVols)
	// make volume search batches so we can parallelize searches
	batchVols := makeBatchesStringPointer(inVolsD, exp.volBatchSize)
	for _, batch := range batchVols {
		exp.log.Info("searching for batch of volumes", "size", len(batch))
		exp.wgv.Add(1)
		go exp.describeVolumes(batch)
	}
	exp.log.Info("Waiting for describeVolume batches to finish")
	exp.wgv.Wait()
	exp.log.Debug("describeVolumes complete", "volumesFound", len(exp.realVols))
	for _, vol := range exp.realVols {
		for _, nug := range nuggets {
			if *vol.VolumeId == *nug.Snap.VolumeId {
				nug.HasVol = true
			}
		}
	}
	exp.Nuggets = append(exp.Nuggets, nuggets...)
}

// Start kicks off the expedition. After this completes
// the data can be exported. 
func (exp *Expedition) Start() (err error) {
	err = exp.setDateFilter(exp.dateFilter)
	if err != nil {
		exp.log.Error("error parsing desired date filter, exiting", "error", err.Error())
		return err
	}
	exp.log.Debug("set datefilter", "exp.cutoffDate", exp.cutoffDate)
	err = exp.getAccountNumber()
	if err != nil {
		return err
	}
	err = exp.getSnapshots()
	if err != nil {
		return err
	}
	// now find out if snapshots are used in images, launch configs/templates, or ASGs
	err = exp.populateNuggets()
	if err != nil {
		if strings.Contains(err.Error(), "RequestLimitExceeded") {
			exp.log.Warn("detected RequestLimitExceeded. Try adjusting VolumeBatchSize higher")
		}
		return err
	}
	// build bars
	exp.addBars()
	exp.setRecommendations()
	return err
}

// snapshotSharedTo takes a session and an snapshot ID string and looks up the sharing
// properties of that snapshot. It returns a slice of account number strings where
// the image is shared and any error. If the image is "public" it returns a slice of length
// 1 with the only item being "all".
func (exp *Expedition) snapshotSharedTo(snap string) (accts []string, err error) {
	exp.log.Debug("describing snapshot attributes for sharing", "snapshot", snap)
	svc := ec2.New(exp.session)
	createVolumePermissionAttr := "createVolumePermission"
	input := ec2.DescribeSnapshotAttributeInput{
		Attribute:  &createVolumePermissionAttr,
		SnapshotId: &snap,
	}
	results, err := svc.DescribeSnapshotAttribute(&input)
	if err != nil {
		return accts, err
	}
	for _, perm := range results.CreateVolumePermissions {
		if perm.Group != nil {
			// catch case where snapshot shared public as string "all" I think
			accts = append(accts, *perm.Group)
		}
		if perm.UserId != nil {
			// grab acct nums
			accts = append(accts, *perm.UserId)
		}
	}
	return accts, err
}

// setDefaultLogger just sets up a logger for the Expedition
// set to Info and stdout by default.
func (exp *Expedition) setDefaultLogger() {
	exp.log = log15.New()
	exp.log.SetHandler(
		log15.LvlFilterHandler(
			log15.LvlInfo,
			log15.StreamHandler(os.Stdout, log15.LogfmtFormat()),
		),
	)
}

// ExpeditionInput provides configuration inputs for starting
// a new Expedition to analyze orphaned snapshots.
type ExpeditionInput struct {
	// AWS Session to use for credentials for this
	// expedition.
	//
	// Session is a required field
	Session *session.Session

	// Maximum number of pages of snapshots to process
	// from the describeSnapshots operation
	// Default: 25
	MaxPages *int

	// Maximum number of snapshots per page
	// Default: 500
	PageSize *int

	// When describing volumes to see if they exist
	// for a given snapshot each volume must be described
	// individually. This is fanned out to multiple
	// goroutines each with a batch of volumes to describe.
	//
	// Adjust this up if you hit throttling errors and down
	// if you want to improve speed.
	// Default: 30
	VolumeBatchSize *int

	// All snapshots created after DateFilter will be
	// ignored in the analysis. Format "YYYY-MM-DD"
	// Default: "2019-01-01"
	DateFilter *string

	// If the ExportRecommendations method is called on the returned
	// Expedition it will write an analysis summary to the
	// OutfileRecommendations filename in text format.
	// Default: "outfile-summary.txt"
	OutfileRecommendations *string

	// If the ExportNuggets method is called on the returned
	// Expedition it will write all Nuggets (snapshots with
	// extra metadata) to the OutfileNuggets filename in csv format.
	// Default: "outfile-nuggets.csv"
	OutfileNuggets *string

	// If the ExportBars method is called on the returned
	// Expedition it will write all bars (snapshots aggregated
	// by volume) to the OutfileBars filename in csv format.
	// Default: "outfile-bars.csv"
	OutfileBars *string

	// Expedition uses log15 (https://github.com/inconshreveable/log15)
	// as an opinioned logging framework. If no Logger is provided
	// Expedition will set up its own handler to stdout.
	Logger *log15.Logger

	// This is the EBS Snapshot storage rate used in calculating
	// savings estimate.
	// Default: 0.05
	EbsSnapRate *float64
}

// New returns a Expedition object whose methods can be called to perform
// an orphaned snapshot analysis. This method accepts an ExpeditionInput
// struct which can be used to setup the Expedition inputs. This method
// will set any default values for any property that was not specified
// in the ExpeditionInput object.
func New(input *ExpeditionInput) (exp *Expedition, err error) {
	var e Expedition

	DefaultDateFilter := "2019-01-01"
	if input.DateFilter == nil {
		input.DateFilter = &DefaultDateFilter
	}
	e.dateFilter = *input.DateFilter

	if input.Session == nil {
		err = errors.New("Session is required")
		return &e, err
	}
	e.session = input.Session

	DefaultMaxPages := 25
	if input.MaxPages == nil {
		input.MaxPages = &DefaultMaxPages
	}
	e.maxPages = *input.MaxPages

	DefaultPageSize := 500
	if input.PageSize == nil {
		input.PageSize = &DefaultPageSize
	}
	e.pageSize = *input.PageSize

	DefaultVolumeBatchSize := 30
	if input.VolumeBatchSize == nil {
		input.VolumeBatchSize = &DefaultVolumeBatchSize
	}
	e.volBatchSize = *input.VolumeBatchSize

	DefaultOutfileRecommendations := "out-summary.txt"
	if input.OutfileRecommendations == nil {
		input.OutfileRecommendations = &DefaultOutfileRecommendations
	}
	e.outfileRecommendations = *input.OutfileRecommendations

	DefaultOutfileNuggets := "out-nuggets.csv"
	if input.OutfileNuggets == nil {
		input.OutfileNuggets = &DefaultOutfileNuggets
	}
	e.outfileNuggets = *input.OutfileNuggets

	DefaultOutfileBars := "out-bars.csv"
	if input.OutfileBars == nil {
		input.OutfileBars = &DefaultOutfileBars
	}
	e.outfileBars = *input.OutfileBars

	if input.Logger == nil {
		err = errors.New("log15 logger is required")
		return &e, err
	}
	e.log = *input.Logger

	DefaultEbsSnapRate := 0.05
	if input.EbsSnapRate == nil {
		input.EbsSnapRate = &DefaultEbsSnapRate
	}
	e.ebsSnapRate = *input.EbsSnapRate
	return &e, err
}
