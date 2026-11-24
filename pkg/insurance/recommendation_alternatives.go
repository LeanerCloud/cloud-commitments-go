package insurance

func associateCandidate(r *RecommendationIdentity, c *CandidateIdentity) CandidateAssociation {
	out := CandidateAssociation{OfferID: c.OfferID, Status: CandidateUnavailable, Reason: ReasonCandidateBasisUnproven, NativeIdentityStatus: CandidateUnavailable}
	if validMatchUUID(c.OfferID) && c.Provider == r.Provider && c.Type == commitmentSavingsPlan && validPayment(c.Payment) && sameAmount(r.HourlyCommitment, c.SelectedAmount) {
		out.Status, out.Reason = CandidateVendorAlternative, ""
	}
	return out
}
