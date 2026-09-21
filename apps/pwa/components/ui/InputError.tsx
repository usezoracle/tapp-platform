"use client";

import { motion } from "framer-motion";
import { TiInfo } from "react-icons/ti";

export function InputError({ message }: { message: string }) {
  return (
    <motion.p
      initial={{ opacity: 0, y: -10 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: -10 }}
      transition={{ duration: 0.2 }}
      className="flex items-center gap-1 text-xs font-medium text-negative-fg [&>svg]:size-3.5"
      role="alert"
    >
      <TiInfo className="text-sm" />
      <span>{message}</span>
    </motion.p>
  );
}
