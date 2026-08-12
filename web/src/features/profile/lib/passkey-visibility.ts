type PasskeyStatus = {
  passkey_login?: boolean
}

export function shouldShowPasskeyCard(
  status: PasskeyStatus | null | undefined
): boolean {
  return status?.passkey_login === true
}
