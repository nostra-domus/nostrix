# mDNS: broadcast hostname.local on the local network via Avahi.
# Lets users reach the server as hostname.local without configuring DNS.
{ ... }:
{
  services.avahi = {
    enable    = true;
    nssmdns4  = true;
    publish = {
      enable      = true;
      addresses   = true;
      workstation = true;
    };
  };

  # mDNS uses UDP port 5353.
  networking.firewall.allowedUDPPorts = [ 5353 ];
}
