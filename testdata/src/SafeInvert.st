// SafeInvert – merkinvaihto ilman ylivuotoa
//
// INT: alue -32768 .. 32767
// SafeInvert(-32768) = 32767  (rajataan ylärajaan, koska 32768 ylivuotaisi)
// Kaikki muut arvot: tulos = -arvo (normaali negaatio).

FUNCTION SafeInvert : INT
VAR_INPUT
    value : INT;
END_VAR
IF value = -32768 THEN
    SafeInvert := 32767;
ELSE
    SafeInvert := 0 - value;
END_IF;
END_FUNCTION
