"""
One-time script to generate the real bogie TDS Declaration PDF
(Declaration U/S 393(1)/393(4) of the Income Tax Act, 2025 for goods-carriage
contractors), replacing the earlier generic placeholder.

Output: backend/static/policies/tds-declaration.pdf
"""
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle
from reportlab.lib.units import mm
from reportlab.lib.enums import TA_CENTER, TA_LEFT
from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer, ListFlowable, ListItem
from reportlab.lib import colors

OUT_DIR = "c:/Users/ADMIN/OneDrive/Desktop/gogoo/backend/static/policies"

styles = getSampleStyleSheet()
body_style = ParagraphStyle("BodyX", parent=styles["Normal"], fontSize=10.5, leading=15, spaceAfter=8, alignment=TA_LEFT)
field_style = ParagraphStyle("FieldX", parent=body_style, spaceAfter=6)
heading_style = ParagraphStyle(
    "HeadX", parent=styles["Normal"], fontSize=11, leading=16, spaceBefore=6, spaceAfter=10,
    alignment=TA_CENTER, fontName="Helvetica-Bold",
)
bullet_style = ParagraphStyle("BulletX", parent=body_style, spaceAfter=6)
footer_style = ParagraphStyle("FooterX", parent=styles["Normal"], fontSize=8, textColor=colors.grey, alignment=TA_CENTER, spaceBefore=24)

HL = 'backColor="#FFF59D"'  # placeholder highlight, matches source doc's yellow fill


def hl(text):
    return f'<font {HL}>{text}</font>'


def esc(t):
    return t.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


doc = SimpleDocTemplate(
    f"{OUT_DIR}/tds-declaration.pdf", pagesize=A4,
    topMargin=22 * mm, bottomMargin=18 * mm, leftMargin=22 * mm, rightMargin=22 * mm,
    title="bogie TDS Declaration",
)

story = [
    Paragraph("To,", body_style),
    Paragraph(hl("<b>[bogie legal entity name, e.g. Bogie Technologies Private Limited]</b>,"), field_style),
    Paragraph(hl("[Registered office address line 1],"), field_style),
    Paragraph(hl("[Address line 2],"), field_style),
    Paragraph(hl("[City - PIN, State]"), field_style),
    Paragraph(f'PAN: {hl("[Company PAN]")}', field_style),
    Paragraph(f'TAN: {hl("[Company TAN]")}', field_style),
    Spacer(1, 10),
    Paragraph("Dear Sir,", body_style),
    Spacer(1, 4),
    Paragraph(
        "<u>Declaration U/S 393(1) [Table: S. No. 6(i)], read with 393(4) [Table: S. No. 8] of the "
        "Income Tax Act, 2025 (‘Act’)</u>",
        heading_style,
    ),
    Paragraph("I/ We,", body_style),
    Paragraph(f'<b>Name of declarant (‘Contractor’):</b> ({hl("‘PAN Card Name’")})', field_style),
    Paragraph(f'<b>{hl("Permanent Account Number (‘PAN’):")}</b>', field_style),
    Paragraph(f'<b>{hl("Address:")}</b>', field_style),
    Paragraph(
        "do hereby make the following declaration as required under section 393(1) [Table: S. No. 6(i)], read "
        "with 393(4) [Table: S. No. 8] of the Income Tax Act, 2025 (‘Act’) for receiving payments from "
        + hl("[bogie legal entity name]") + " (‘Company’) without deduction of tax at source: -",
        body_style,
    ),
]

declarations = [
    "That I/We am/are authorized to make this declaration in the capacity as Proprietor/Partner/Director.",
    "The contractor is engaged in the business of plying, hiring, or leasing of goods carriages.",
    "That the contractor does not own/ has not owned/ does not intend to own more than ten (10) goods "
    "carriages at any time during the tax year 2026-27.",
    "That the contractor re-affirms that the contractor has not owned more than ten (10) goods carriages at "
    "any time during the preceding tax year 2025-26.",
    "That the contractor is eligible to claim benefit of Section 58(2) [Table: S. No. 2] of the Act.",
    "That the contractor is engaged by the Company for plying, hiring, or leasing of goods carriage for the "
    "Company’s business.",
    "That if the number of goods carriages owned by the contractor exceeds ten (10) after furnishing this "
    "declaration, the contractor shall forthwith, in writing intimate the payer of this fact.",
    "That the PAN of the contractor furnished to the Company is valid and operative.",
]
items = [ListItem(Paragraph(esc(d), bullet_style)) for d in declarations]
story.append(ListFlowable(items, bulletType="1", leftIndent=16, spaceAfter=10))

story += [
    Paragraph(
        "I/ we hereby affirm that the declarations are true and correct to the best of my/ our knowledge and "
        "belief. I/We shall be held responsible for any adverse consequences arising due to any incorrect "
        "declarations.",
        body_style,
    ),
    Spacer(1, 8),
    Paragraph(
        "Date: " + hl("For all existing Partners: 1st April 2026. For new onboarding after 31st March 2026, "
                      "joining date only"),
        field_style,
    ),
    Paragraph(f'Place: {hl("City Name")}', field_style),
    Paragraph(f'Declarant Name: {hl("&lt; PAN Card Name &gt;")}', field_style),
    Spacer(1, 14),
    Paragraph(hl("IP Address"), field_style),
    Paragraph(hl("Time Stamp"), field_style),
    Spacer(1, 10),
    Paragraph(
        "Company: Aggarwal Publicity and Marketing Pvt. Ltd., Delhi NCR, India | Platform: bogie | Version 1.0",
        footer_style,
    ),
]

doc.build(story)
print("wrote tds-declaration.pdf (real declaration)")
