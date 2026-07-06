package configver

// Fault-injection hook for the publish-crash chaos drill (doc 13 §7 "dashboardd kill during
// publish"; closes OI-P12-3 / OI-TS-5).
//
// PublishFaultAfterPointer is a TEST-ONLY seam. When a test assigns it, PGStore.Publish invokes
// it INSIDE the publish transaction — immediately after config_active has been flipped to the new
// version but BEFORE the transaction commits (see pgstore.go step 2b). A test uses it to crash /
// kill the transaction mid-publish and then assert that config_active is left consistent (the old
// pointer or a fully-published version, never a dangling pointer to a non-validated version) and
// that config_epochs is not double-bumped.
//
// Why a package var and NOT a debug endpoint (the OI-TS-5 decision): the hook has no runtime
// surface. It defaults to nil, and only in-process test code can assign it — there is no env var,
// flag, config field, or HTTP route that sets it, so a production dashboardd build can never fire
// it. The nil check below is the guard; production leaves it nil forever, making the fault point
// inert and impossible to trigger from outside the binary. Keeping it here (rather than a
// //go:build hook) means the exported symbol is available to the external configver_test package
// while the guard keeps it dormant everywhere else.
var PublishFaultAfterPointer func()

// firePublishFault invokes the test-only fault hook if (and only if) a test has assigned it.
// Inlined callers stay readable; production builds skip it on the nil check.
func firePublishFault() {
	if PublishFaultAfterPointer != nil {
		PublishFaultAfterPointer()
	}
}
