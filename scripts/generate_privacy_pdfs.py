"""
One-time script to generate bogie's Privacy Policy PDF set:
  - privacy-policy.pdf           (index, links out to the two below)
  - privacy-policy-riders.pdf
  - privacy-policy-drivers.pdf

Run once, output goes to backend/static/policies/. Content is original,
bogie-specific text — not copied from any third-party policy.
"""
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle
from reportlab.lib.units import mm
from reportlab.lib.enums import TA_CENTER
from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer, ListFlowable, ListItem
from reportlab.lib import colors

OUT_DIR = "c:/Users/ADMIN/OneDrive/Desktop/gogoo/backend/static/policies"
RIDERS_URL = "https://gogobackend-production.up.railway.app/policies/privacy-policy-riders.pdf"
DRIVERS_URL = "https://gogobackend-production.up.railway.app/policies/privacy-policy-drivers.pdf"

styles = getSampleStyleSheet()
title_style = ParagraphStyle("TitleX", parent=styles["Title"], fontSize=20, spaceAfter=4)
sub_style = ParagraphStyle("SubX", parent=styles["Normal"], fontSize=10, textColor=colors.grey, alignment=TA_CENTER, spaceAfter=16)
h2_style = ParagraphStyle("H2X", parent=styles["Heading2"], fontSize=13, spaceBefore=14, spaceAfter=6)
body_style = ParagraphStyle("BodyX", parent=styles["Normal"], fontSize=10, leading=15, spaceAfter=8, alignment=4)
bullet_style = ParagraphStyle("BulletX", parent=body_style, spaceAfter=4)
footer_style = ParagraphStyle("FooterX", parent=styles["Normal"], fontSize=8, textColor=colors.grey, alignment=TA_CENTER, spaceBefore=20)
link_style = ParagraphStyle("LinkX", parent=styles["Normal"], fontSize=14, textColor=colors.HexColor("#1a4fd6"), spaceAfter=14, leading=20)


def esc(t):
    return t.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def build_pdf(filename, title, subtitle, sections, footer):
    doc = SimpleDocTemplate(
        f"{OUT_DIR}/{filename}", pagesize=A4,
        topMargin=22 * mm, bottomMargin=18 * mm, leftMargin=20 * mm, rightMargin=20 * mm,
        title=title,
    )
    story = [Paragraph(esc(title), title_style), Paragraph(esc(subtitle), sub_style)]
    for heading, paras in sections:
        if heading:
            story.append(Paragraph(esc(heading), h2_style))
        for p in paras:
            if isinstance(p, list):
                items = [ListItem(Paragraph(esc(b), bullet_style)) for b in p]
                story.append(ListFlowable(items, bulletType="bullet", leftIndent=14, spaceAfter=8))
            else:
                story.append(Paragraph(esc(p), body_style))
    story.append(Spacer(1, 10))
    story.append(Paragraph(esc(footer), footer_style))
    doc.build(story)
    print("wrote", filename)


# ---------------------------------------------------------------------------
# 1. INDEX PDF
# ---------------------------------------------------------------------------
def build_index():
    doc = SimpleDocTemplate(
        f"{OUT_DIR}/privacy-policy.pdf", pagesize=A4,
        topMargin=30 * mm, bottomMargin=18 * mm, leftMargin=20 * mm, rightMargin=20 * mm,
        title="bogie Privacy Policy",
    )
    story = [
        Paragraph("Privacy Policy", title_style),
        Spacer(1, 10),
        Paragraph(
            "Aggarwal Publicity and Marketing Pvt. Ltd. (“bogie”), Delhi NCR, India",
            ParagraphStyle("company", parent=styles["Normal"], fontSize=11, textColor=colors.grey, spaceAfter=28),
        ),
        Paragraph(f'(i) <a href="{RIDERS_URL}"><u>Privacy Policy for bogie Riders</u></a>', link_style),
        Paragraph(f'(ii) <a href="{DRIVERS_URL}"><u>Privacy Policy for bogie Driver Partners</u></a>', link_style),
        Spacer(1, 20),
        Paragraph(
            "Please open the policy relevant to how you use bogie — as a Rider booking cab, "
            "truck or ambulance services through the bogie user app, or as a Driver Partner "
            "providing services through the bogie Driver app.",
            body_style,
        ),
        Spacer(1, 30),
        Paragraph(
            "Company: Aggarwal Publicity and Marketing Pvt. Ltd., Delhi NCR, India | Platform: bogie | Version 1.0",
            footer_style,
        ),
    ]
    doc.build(story)
    print("wrote privacy-policy.pdf (index)")


build_index()

# ---------------------------------------------------------------------------
# 2. RIDERS POLICY
# ---------------------------------------------------------------------------
riders_sections = [
    (None, [
        'This Privacy Policy explains how Aggarwal Publicity and Marketing Pvt. Ltd. ("bogie", "we", "us") '
        'collects, uses, shares and protects the personal data of Riders who use the bogie user application '
        'to book cab, truck/goods transportation, or ambulance services (the "Services") in Delhi NCR and other '
        'serviceable areas.',
        'This Policy is an electronic record under the Information Technology Act, 2000 and forms part of, and is '
        'to be read together with, the bogie Terms of Service. By creating an account or using the bogie app, you '
        'agree to the collection and use of your information as described here. If you do not agree, please do '
        'not use the bogie app.',
    ]),
    ("1. Information We Collect", [
        'Personal details:',
        [
            'Name, profile photo, mobile number and email address',
            'Home and other saved addresses',
        ],
        'Location:',
        [
            'Precise GPS location while a booking is being made and during an active ride, for pickup, drop-off '
            'and live trip tracking',
            'Location history relating to your bookings, retained for a limited period as described in Section 6',
        ],
        'Booking and payment:',
        [
            'Booking history, fare and payment references (bogie does not store your card or bank details — these '
            'are handled by our payment processing partners)',
        ],
        'Device and usage:',
        [
            'Device model, OS version and app version',
            'App usage analytics collected via Google Firebase, to understand feature usage and app stability',
            'Crash logs and performance diagnostics',
            'Push-notification token, to send booking updates and alerts',
        ],
        'Support and safety:',
        [
            'Contents of in-app support chat messages, including messages processed by our AI-assisted support '
            'system to help respond to your queries',
            'Information generated when you use the SOS/emergency alert feature, including your live location at '
            'the time of the alert',
            'Referral codes used or shared, and rewards earned, if you participate in the bogie referral programme',
        ],
    ]),
    ("2. How We Use Your Information", [
        [
            'Match you with a nearby, available Driver Partner for cab, truck or ambulance bookings',
            'Calculate fares, process payments and generate receipts',
            'Share live trip tracking with you (and, where you use SOS, with your emergency contacts and bogie '
            'support) for safety',
            'Send booking confirmations, OTPs, ride status updates and service notifications',
            'Route and respond to your queries through in-app support chat, including AI-assisted responses',
            'Administer the bogie referral programme, including tracking codes and crediting rewards',
            'Improve app performance, fix bugs and develop new features',
            'Detect and prevent fraud and misuse of the platform',
            'Comply with Applicable Law, including requests from courts, police or regulatory authorities',
            'Send you promotional offers and updates, where you have not opted out',
        ],
    ]),
    ("3. Location Data", [
        [
            'Precise location is collected only while the app is in use for making or tracking a booking',
            'During an active ride, your location is shared with the Driver Partner assigned to your booking',
            'Location data may be processed through our maps and navigation provider (Ola Maps) to calculate '
            'routes, ETAs and fares',
            'Disabling location permissions will prevent core booking and tracking features from working',
        ],
    ]),
    ("4. How We Share Your Information", [
        '4.1 With Driver Partners, hospitals or NGOs: to the extent necessary to fulfil your booking, we share '
        'your name, pickup/drop location and contact details with the Driver Partner assigned to your Order, and '
        '(for ambulance bookings) with the relevant partner hospital or NGO.',
        '4.2 With service providers: we share data with vendors who support our operations, such as cloud hosting '
        'providers, Ola Maps (routing and navigation), Google Firebase (analytics, crash reporting and push '
        'notifications), payment processing partners, and AI-assisted support-chat providers. These providers may '
        'access only the data needed to perform their function for bogie.',
        '4.3 With authorities: we may disclose information to government, regulatory or law-enforcement bodies '
        'where required under Applicable Law, or in good faith to prevent fraud, protect safety, or comply with a '
        'legal process.',
        '4.4 We do not sell your personal data to advertisers or other third parties, and we do not use it for '
        'profiling outside of the bogie platform.',
    ]),
    ("5. Permissions Used by the App", [
        [
            'Location — to find nearby drivers, enable live tracking and calculate fares',
            'Contacts — accessed only if you choose to share a specific number for referrals or emergency contacts; '
            'we do not upload your full contact list',
            'Camera — to capture photos where required (e.g., consignment photos, profile picture)',
            'Microphone — for in-app voice/support features, where used',
            'Notifications — to deliver booking updates, OTPs and safety alerts',
        ],
    ]),
    ("6. Data Retention", [
        [
            'Account data is retained while your account remains active',
            'Booking and fare history is retained for a period consistent with GST and accounting requirements',
            'Location history related to bookings is retained for a limited period and then purged',
            'On account deletion, personal data is removed within a reasonable period, except where retention is '
            'required for legal, safety or fraud-prevention purposes',
        ],
    ]),
    ("7. Data Security", [
        [
            'Data in transit is protected using industry-standard encryption (TLS)',
            'Passwords are stored as salted hashes, never in plain text',
            'Access to personal data is restricted to authorised personnel on a need-to-know basis',
            'We take reasonable technical and organisational measures to protect your data against unauthorised '
            'access, loss or misuse',
        ],
    ]),
    ("8. Your Rights (Digital Personal Data Protection Act, 2023)", [
        [
            'Right to access the personal data we hold about you',
            'Right to correct inaccurate or incomplete data',
            'Right to request erasure of your data, subject to legal retention requirements',
            'Right to know how your data is being processed',
            'Right to withdraw consent at any time',
            'Right to nominate a representative to exercise these rights on your behalf',
            'Right to file a grievance with our Grievance Officer (Section 11)',
        ],
        'To exercise any of these rights, write to privacy@bogie.in. We aim to respond within 7 working days.',
    ]),
    ("9. Children's Privacy", [
        'bogie Services are intended for users who are 18 years of age or older. We do not knowingly collect '
        'personal data from minors. If you believe a minor has registered an account, please contact '
        'privacy@bogie.in so that we can take appropriate action.',
    ]),
    ("10. Changes to this Policy", [
        'We may update this Privacy Policy from time to time to reflect changes in our practices or Applicable '
        'Law. Material changes will be notified through the bogie app. Your continued use of the app after an '
        'update constitutes acceptance of the revised Policy.',
    ]),
    ("11. Grievance Officer and Contact", [
        [
            'Grievance Officer: Anjali Aggarwal, Data Protection Officer',
            'Company: Aggarwal Publicity and Marketing Pvt. Ltd.',
            'Address: New Delhi, Delhi – 110001, India',
            'Email: privacy@bogie.in',
            'General support: support@bogie.in, or the in-app support chat',
        ],
    ]),
]

build_pdf(
    "privacy-policy-riders.pdf",
    "BOGIE PRIVACY POLICY",
    "For Riders — bogie User App",
    riders_sections,
    "Company: Aggarwal Publicity and Marketing Pvt. Ltd., Delhi NCR, India | Platform: bogie | Version 1.0",
)

# ---------------------------------------------------------------------------
# 3. DRIVER PARTNERS POLICY
# ---------------------------------------------------------------------------
drivers_sections = [
    (None, [
        'This Privacy Policy explains how Aggarwal Publicity and Marketing Pvt. Ltd. ("bogie", "we", "us") '
        'collects, uses, shares and protects the personal data of Driver Partners who register on and use the '
        'bogie Driver application (the "Driver App") to provide cab, truck/goods transportation or ambulance '
        'services through the bogie platform.',
        'This Policy is an electronic record under the Information Technology Act, 2000 and forms part of, and is '
        'to be read together with, the bogie Driver Partner Terms and Conditions. By registering on the Driver App '
        'or accepting an Order, you agree to the collection and use of your information as described here. If you '
        'do not agree, please do not register on or use the Driver App.',
    ]),
    ("1. Information We Collect", [
        'Identity and KYC documents:',
        [
            'Name, profile photo, mobile number and email address',
            'Aadhaar, PAN, driving licence, vehicle registration certificate (RC), insurance and pollution '
            'certificate, and any other documents required for onboarding and verification',
            'Background verification and identity-check information, which may be collected through an authorised '
            'third-party vendor on our behalf',
        ],
        'Location:',
        [
            'Location while you are online and available for Orders, and continuously during an active Order, to '
            'enable Order assignment, live tracking and safety features',
            'Location shared with Customers, hospitals or NGOs as necessary to fulfil an Order',
        ],
        'Earnings and tax:',
        [
            'Wallet balance, ledger entries, Driver Earnings and payout/bank account details',
            'PAN and TDS Declaration details, used to determine and record tax deducted at source as required by '
            'Applicable Law',
        ],
        'Device and usage:',
        [
            'Device model, OS version and app version',
            'App usage analytics collected via Google Firebase, to understand feature usage and app stability',
            'Crash logs and performance diagnostics',
            'Push-notification token, to send Order alerts and account notifications',
            'Battery-level information, read at points relevant to Order eligibility and driver availability (for '
            'example, to avoid assigning Orders to a device likely to lose power mid-trip)',
        ],
        'Support and safety:',
        [
            'Contents of in-app support chat messages, including messages processed by our AI-assisted support '
            'system to help respond to your queries',
            'Information generated when you use the SOS/emergency alert feature, including your live location at '
            'the time of the alert',
            'Photos captured through the Driver App where requested (for example, consignment photos or a selfie '
            'for identity confirmation)',
        ],
    ]),
    ("2. How We Use Your Information", [
        [
            'Verify your identity, documents and eligibility to operate on the bogie platform',
            'Assign you Orders and enable navigation, live tracking and Order completion',
            'Calculate Driver Earnings, maintain your Wallet/ledger, and process payouts',
            'Deduct and record TDS in accordance with the TDS Declaration you provide and Applicable Law',
            'Share your name, phone number, vehicle details and photo with Customers during an active Order, so '
            'they can identify you',
            'Provide safety features, including SOS alerts and sharing your location with bogie support and, '
            'where relevant, emergency services',
            'Route and respond to your queries through in-app support chat, including AI-assisted responses',
            'Improve app performance, fix bugs and develop new features',
            'Detect and prevent fraud, including misuse of incentives, multiple accounts or document tampering',
            'Comply with Applicable Law, including the Motor Vehicles Act, GST law and requests from courts, '
            'police or regulatory authorities',
        ],
    ]),
    ("3. Location Data", [
        [
            'Collected while you are online in the Driver App and continuously during an active Order, as required '
            'under the Driver Partner Terms and Conditions',
            'Shared with the Customer during an active Order, and processed through our maps and navigation '
            'provider (Ola Maps) for routing and ETAs',
            'Keeping location/GPS services enabled is required to accept and perform Orders; disabling it may '
            'prevent Order allocation',
        ],
    ]),
    ("4. How We Share Your Information", [
        '4.1 With Customers: your name, phone number (masked where applicable), vehicle details and photo are '
        'shared with the Customer for an Order so they can identify you and coordinate pickup/drop.',
        '4.2 With service providers: we share data with vendors who support our operations, such as cloud hosting '
        'providers, Ola Maps (routing and navigation), Google Firebase (analytics, crash reporting and push '
        'notifications), payment/payout processing partners, background-verification vendors, and AI-assisted '
        'support-chat providers. These providers may access only the data needed to perform their function for '
        'bogie.',
        '4.3 With authorities: we may disclose information to government, regulatory, tax or law-enforcement '
        'bodies where required under Applicable Law (including for TDS/GST compliance, RTO checks or police '
        'inquiries), or in good faith to prevent fraud or protect safety.',
        '4.4 We do not sell your personal data to advertisers or other third parties.',
    ]),
    ("5. Permissions Used by the App", [
        [
            'Location — to receive and perform Orders, enable live tracking and navigation',
            'Camera — to capture consignment photos, a verification selfie, or document images where requested',
            'Contacts — accessed only if you choose to share a specific number for referrals; we do not upload '
            'your full contact list',
            'Microphone — for in-app voice/support features, where used',
            'Notifications — to deliver Order alerts, wallet updates and safety alerts',
            'Background/battery status — to determine Order eligibility and maintain reliable tracking while '
            'online',
        ],
    ]),
    ("6. Data Retention", [
        [
            'Account and document data is retained while your account is active and as required under the Motor '
            'Vehicles Act and related regulations',
            'Earnings, Wallet and ledger records are retained for the period required for tax and accounting '
            'compliance',
            'KYC documents are retained as required for regulatory and safety purposes, even after account '
            'closure, where legally necessary',
            'On account deletion, personal data is removed within a reasonable period, except where retention is '
            'required for legal, safety, tax or fraud-prevention purposes',
        ],
    ]),
    ("7. Data Security", [
        [
            'KYC and identity documents are encrypted at rest',
            'Data in transit is protected using industry-standard encryption (TLS)',
            'Passwords are stored as salted hashes, never in plain text',
            'Bank/payout details are stored using bank-grade encryption',
            'Access to personal data is restricted to authorised personnel on a need-to-know basis',
        ],
    ]),
    ("8. Your Rights (Digital Personal Data Protection Act, 2023)", [
        [
            'Right to access the personal data we hold about you',
            'Right to correct inaccurate or incomplete data',
            'Right to request erasure of your data, subject to legal and regulatory retention requirements',
            'Right to know how your data is being processed',
            'Right to withdraw consent at any time (which may affect your ability to receive Orders)',
            'Right to nominate a representative to exercise these rights on your behalf',
            'Right to file a grievance with our Grievance Officer (Section 11)',
        ],
        'To exercise any of these rights, write to privacy@bogie.in. We aim to respond within 7 working days.',
    ]),
    ("9. Children's Privacy", [
        'The Driver App is intended only for users who are 18 years of age or older, consistent with the Driver '
        'Partner Terms and Conditions. We do not knowingly collect personal data from minors.',
    ]),
    ("10. Changes to this Policy", [
        'We may update this Privacy Policy from time to time to reflect changes in our practices or Applicable '
        'Law. Material changes will be notified through the Driver App. Your continued use of the Driver App '
        'after an update constitutes acceptance of the revised Policy.',
    ]),
    ("11. Grievance Officer and Contact", [
        [
            'Grievance Officer: Anjali Aggarwal, Data Protection Officer',
            'Company: Aggarwal Publicity and Marketing Pvt. Ltd.',
            'Address: New Delhi, Delhi – 110001, India',
            'Email: privacy@bogie.in',
            'Driver support: driver-support@bogie.in, or the in-app support chat',
        ],
    ]),
]

build_pdf(
    "privacy-policy-drivers.pdf",
    "BOGIE PRIVACY POLICY",
    "For Driver Partners — bogie Driver App",
    drivers_sections,
    "Company: Aggarwal Publicity and Marketing Pvt. Ltd., Delhi NCR, India | Platform: bogie | Version 1.0",
)
