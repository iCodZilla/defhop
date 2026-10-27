package demo;

import java.util.function.Function;

public class ProposalRef {
    public Function<PricingProposal, String> codeOf() {
        return PricingProposal::getCInquiryCode;
    }
}
